package biz

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"yola/test/whot/internal/biz/player"
	"yola/test/whot/internal/biz/robot"
	"yola/test/whot/internal/biz/table"
	"yola/test/whot/internal/conf"

	"github.com/google/wire"
)

// 实现table.Repo接口
var _ table.Repo = (*Usecase)(nil)

// 实现robot.Repo接口
var _ robot.Repo = (*Usecase)(nil)

const defaultStatusInterval = 120 * time.Second

var (
	ErrPlayerNotFound     = errors.New("player not found")
	ErrInvalidPlayerState = errors.New("invalid player state")
	ErrReadyRejected      = errors.New("ready request rejected")
	ErrChatRejected       = errors.New("chat request rejected")
	ErrHostingRejected    = errors.New("hosting request rejected")
)

// ProviderSet wires the Whot business use case.
var ProviderSet = wire.NewSet(NewUsecase)

// PlayerRepo persists player state at the business boundary.
type PlayerRepo interface {
	SavePlayer(ctx context.Context, p *player.BaseData) error
	LoadPlayer(ctx context.Context, playerID int64) (*player.BaseData, error)
}

// Usecase owns the Whot player, robot, and Table lifecycle.
type Usecase struct {
	repo PlayerRepo
	rc   *conf.Room

	pm           *player.Manager
	tm           *table.Manager
	rm           *robot.Manager
	cancel       context.CancelFunc
	backgroundWG sync.WaitGroup

	drainOnce sync.Once
	drainErr  error
}

// NewUsecase new a data usecase.
func NewUsecase(repo PlayerRepo, c *conf.Room) (*Usecase, func(), error) {
	if repo == nil {
		return nil, nil, errors.New("player repo is required")
	}
	ctx, cancel := context.WithCancel(context.Background())
	uc := &Usecase{repo: repo, rc: c, cancel: cancel}

	uc.tm = table.NewManager(c, uc)
	uc.pm = player.NewManager()
	uc.rm = robot.NewManager(c, uc)

	cleanup := func() {
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), playerCleanupTimeout)
		defer cancelCleanup()
		if err := uc.Drain(cleanupCtx); err != nil {
			slog.Error("close Whot usecase", "error", err)
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
		return errors.New("drain Whot usecase: context is nil")
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
		uc.drainErr = uc.cleanupPlayers(ctx)
	})
	return uc.drainErr
}

func (uc *Usecase) waitBackground(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		uc.backgroundWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("stop Whot background tasks: %w", ctx.Err())
	}
}

func (uc *Usecase) start(ctx context.Context) error {
	if err := uc.tm.Start(ctx); err != nil {
		return fmt.Errorf("start tables: %w", err)
	}
	uc.backgroundWG.Add(2)
	go func() {
		defer uc.backgroundWG.Done()
		uc.rm.Run(ctx)
	}()
	go func() {
		defer uc.backgroundWG.Done()
		uc.runStatus(ctx)
	}()
	return nil
}

func (uc *Usecase) logStatus() {
	ps := uc.pm.Monitor()
	ai := uc.rm.Monitor()

	slog.Info("game service status", "players", ps, "robots", ai)
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
