package biz

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"yola/test/ludo/internal/biz/player"
	"yola/test/ludo/internal/biz/robot"
	"yola/test/ludo/internal/biz/table"
	"yola/test/ludo/internal/conf"

	"github.com/google/wire"
)

var _ table.Repo = (*Usecase)(nil)

var _ robot.Repo = (*Usecase)(nil)

const defaultStatusInterval = 60 * time.Second

var (
	ErrPlayerNotFound     = errors.New("player not found")
	ErrInvalidPlayerState = errors.New("invalid player state")
	ErrReadyRejected      = errors.New("ready request rejected")
	ErrHostingRejected    = errors.New("hosting request rejected")
)

// ProviderSet wires the Ludo business use case.
var ProviderSet = wire.NewSet(NewUsecase)

// PlayerRepo persists player state at the business boundary.
type PlayerRepo interface {
	SavePlayer(ctx context.Context, p *player.BaseData) error
	LoadPlayer(ctx context.Context, playerID int64) (*player.BaseData, error)
}

// Usecase 负责玩家生命周期和桌路由。
type Usecase struct {
	repo PlayerRepo
	rc   *conf.Room

	pm     *player.Manager
	tm     *table.Manager
	rm     *robot.Manager
	cancel context.CancelFunc
	wg     sync.WaitGroup

	drainOnce sync.Once
	drainErr  error
}

// NewUsecase 构造并启动 Ludo 用例。
func NewUsecase(instanceID string, repo PlayerRepo, rc *conf.Room) (*Usecase, func(), error) {
	if repo == nil {
		return nil, nil, errors.New("player repo is required")
	}
	ctx, cancel := context.WithCancel(context.Background())
	uc := &Usecase{repo: repo, rc: rc, cancel: cancel}
	uc.tm = table.NewManager(instanceID, rc, uc)
	uc.pm = player.NewManager()
	uc.rm = robot.NewManager(rc, uc)

	cleanup := func() {
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), playerCleanupTimeout)
		defer cancelCleanup()
		if err := uc.Drain(cleanupCtx); err != nil {
			slog.Error("close Ludo usecase", "error", err)
		}
	}
	if err := uc.start(ctx); err != nil {
		cleanup()
		return nil, nil, err
	}
	return uc, cleanup, nil
}

// Drain runs once so Node and Wire cleanup cannot repeat partially successful player I/O.
func (uc *Usecase) Drain(ctx context.Context) error {
	if ctx == nil {
		return errors.New("drain Ludo usecase: context is nil")
	}
	uc.drainOnce.Do(func() {
		uc.cancel()
		if err := uc.waitBackground(ctx); err != nil {
			uc.drainErr = err
			return
		}
		if err := uc.tm.Close(ctx); err != nil {
			uc.drainErr = err
			return
		}
		uc.drainErr = uc.closePlayers(ctx)
	})
	return uc.drainErr
}

func (uc *Usecase) waitBackground(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		uc.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("stop Ludo background tasks: %w", ctx.Err())
	}
}

func (uc *Usecase) start(ctx context.Context) error {
	if err := uc.tm.Start(ctx); err != nil {
		return fmt.Errorf("start tables: %w", err)
	}
	uc.wg.Add(2)
	go func() {
		defer uc.wg.Done()
		uc.rm.Run(ctx)
	}()
	go func() {
		defer uc.wg.Done()
		uc.runStatus(ctx)
	}()
	slog.Info("start game service",
		"version", conf.Version,
		"game_id", conf.GameID,
		"arena_id", conf.ArenaID,
	)
	return nil
}

func (uc *Usecase) logStatus() {
	players, offlinePlayers := uc.pm.Counts()
	robots, gamingRobots, freeRobots := uc.rm.Counts()

	slog.Info("game service status",
		"players", players,
		"offline_players", offlinePlayers,
		"robots", robots,
		"gaming_robots", gamingRobots,
		"free_robots", freeRobots,
	)
}

func (uc *Usecase) runStatus(ctx context.Context) {
	ticker := time.NewTicker(defaultStatusInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			uc.logStatus()
		}
	}
}
