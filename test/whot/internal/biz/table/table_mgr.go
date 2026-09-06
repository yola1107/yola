package table

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"yola/test/internal/mailbox"
	"yola/test/internal/timer"
	"yola/test/internal/timer/stdlib"
	"yola/test/whot/internal/biz/player"
	"yola/test/whot/internal/conf"
	"yola/test/whot/pkg/codes"

	"google.golang.org/protobuf/proto"
)

var (
	ErrTableNotFound      = errors.New("table not found")
	ErrPlayerRouteChanged = errors.New("player table route changed")
	ErrPlayerDisconnected = errors.New("player disconnected during table migration")
)

const (
	tableMigrationTimeout      = 2 * time.Second
	defaultTableQueueSize      = 128
	defaultTableMailboxBatch   = 64
	defaultTableMailboxWorkers = 16
)

type Manager struct {
	tables    []*Table
	mailboxes *mailbox.Group
	timers    timer.Scheduler
	runCtx    context.Context

	pusherMu sync.RWMutex
	pusher   ClientPusher
}

type ClientPusher interface {
	PushToUID(ctx context.Context, uid string, command int32, msg proto.Message) error
}

func NewManager(room *conf.Room, repo Repo) *Manager {
	return newManager(room, repo, defaultTableMailboxWorkers, defaultTableQueueSize, defaultTableMailboxBatch)
}

func newManager(room *conf.Room, repo Repo, workers int, queueSize int, batchSize int) *Manager {
	if repo == nil {
		panic("table: repo is required")
	}
	tableCount := int(room.Table.TableNum)
	mailboxes, err := mailbox.NewGroup(tableCount, min(tableCount, workers), queueSize, batchSize)
	if err != nil {
		panic(fmt.Sprintf("table: create mailboxes: %v", err))
	}
	manager := &Manager{
		tables:    make([]*Table, tableCount),
		mailboxes: mailboxes,
		timers:    stdlib.New(),
	}
	for index := range manager.tables {
		tableID := int32(index + 1)
		manager.tables[index] = newTable(tableID, room, repo, manager)
	}
	return manager
}

func (m *Manager) Start(ctx context.Context) error {
	m.runCtx = ctx
	if err := m.mailboxes.Start(); err != nil {
		return fmt.Errorf("start table mailboxes: %w", err)
	}
	if err := m.timers.Start(ctx); err != nil {
		stopErr := m.mailboxes.Stop(context.Background())
		return errors.Join(fmt.Errorf("start table timers: %w", err), stopErr)
	}
	return nil
}

// Close stops timers and drains accepted table work before closing table resources.
func (m *Manager) Close(ctx context.Context) error {
	if ctx == nil {
		return errors.New("close Whot table manager: context is nil")
	}
	timerErr := m.timers.Stop(ctx)
	mailboxErr := m.mailboxes.Stop(ctx)
	if err := errors.Join(timerErr, mailboxErr); err != nil {
		return err
	}
	var tableErr error
	for _, gameTable := range m.tables {
		if gameTable != nil {
			tableErr = errors.Join(tableErr, gameTable.Close())
		}
	}
	return tableErr
}

func (m *Manager) SetClientPusher(pusher ClientPusher) {
	m.pusherMu.Lock()
	defer m.pusherMu.Unlock()
	m.pusher = pusher
}

func (m *Manager) Enter(ctx context.Context, p *player.Player) (int32, string, error) {
	if p == nil {
		return codes.PlayerInvalid, "PLAYER_INVALID", nil
	}
	mailboxBusy := false
	entered, err := m.tryAvailableTables(0, func(target *Table) (bool, error) {
		seated := false
		err := m.call(ctx, target.ID, func(gameTable *Table) error {
			seated = gameTable.Seat(p)
			return nil
		})
		if errors.Is(err, mailbox.ErrFull) {
			mailboxBusy = true
			return false, nil
		}
		return seated, err
	})
	if err != nil {
		return 0, "", err
	}
	if entered {
		return codes.Success, "", nil
	}
	if mailboxBusy {
		return 0, "", mailbox.ErrFull
	}
	return codes.NotEnoughTable, "NOT_ENOUGH_TABLE", nil
}

func (m *Manager) EnterRobots(ctx context.Context, players []*player.Player) ([]int64, error) {
	entered := make([]int64, 0, min(len(players), len(m.tables)))
	tableIndex := 0
	for _, p := range players {
		if p == nil || p.GetTableID() > 0 {
			continue
		}
		for tableIndex < len(m.tables) {
			target := m.tables[tableIndex]
			tableIndex++
			joined := false
			err := m.call(ctx, target.ID, func(gameTable *Table) error {
				if p.GetTableID() > 0 {
					joined = true
					return nil
				}
				if !gameTable.isFull() && gameTable.canEnterRobot(p) {
					joined = gameTable.Seat(p)
				}
				return nil
			})
			if err != nil {
				return entered, fmt.Errorf("enter robot %d table %d: %w", p.GetPlayerID(), target.ID, err)
			}
			if joined {
				entered = append(entered, p.GetPlayerID())
				break
			}
		}
		if tableIndex == len(m.tables) {
			break
		}
	}
	return entered, nil
}

func (m *Manager) Switch(ctx context.Context, p *player.Player, gameConfig *conf.Room_Game) (int32, string, error) {
	if p == nil {
		return codes.PlayerInvalid, "PLAYER_INVALID", nil
	}
	oldTableID := p.GetTableID()
	oldTable := m.table(oldTableID)
	if oldTable == nil {
		return codes.TableNotFound, "TABLE_NOT_FOUND", nil
	}
	if !m.hasAvailableTable(oldTableID) {
		return codes.EnterTableFail, "ENTER_TABLE_FAIL", nil
	}

	code, message := codes.Success, ""
	canSwitch := false
	removed := false
	err := m.CallPlayer(ctx, p, func(current *Table) error {
		code, message = CheckRoomLimit(p, gameConfig)
		if code != codes.Success {
			return nil
		}
		canSwitch = current.canExit(p)
		if canSwitch {
			removed = current.RemovePlayer(p, true)
		}
		return nil
	})
	if err != nil {
		return 0, "", err
	}
	if code != codes.Success {
		return code, message, nil
	}
	if !canSwitch {
		return codes.SwitchTable, "SWITCH_TABLE", nil
	}
	if !removed {
		return codes.ExitTableFail, "EXIT_TABLE_FAIL", nil
	}

	migrationCtx, cancelMigration := context.WithTimeout(context.Background(), tableMigrationTimeout)
	defer cancelMigration()
	entered, err := m.tryAvailableTables(oldTableID, func(target *Table) (bool, error) {
		var seated bool
		migrationErr := m.migrationCall(migrationCtx, target.ID, func(gameTable *Table) error {
			var seatErr error
			seated, seatErr = seatMigratingPlayer(gameTable, p)
			return seatErr
		})
		return seated, migrationErr
	})
	if err != nil {
		if errors.Is(err, ErrPlayerDisconnected) {
			return 0, "", err
		}
		return 0, "", errors.Join(err, m.restorePlayer(oldTable, p))
	}
	if entered {
		return codes.Success, "", nil
	}
	if err = m.restorePlayer(oldTable, p); err != nil {
		return 0, "", err
	}
	return codes.EnterTableFail, "ENTER_TABLE_FAIL", nil
}

func (m *Manager) CallPlayer(ctx context.Context, p *player.Player, action func(*Table) error) error {
	if p == nil {
		return errors.New("table player call requires player")
	}
	if action == nil {
		return errors.New("table player call action is nil")
	}
	tableID := p.GetTableID()
	return m.call(ctx, tableID, func(gameTable *Table) error {
		if p.GetTableID() != tableID || gameTable.playerAt(p.GetChairID()) != p {
			return ErrPlayerRouteChanged
		}
		return action(gameTable)
	})
}

func CheckRoomLimit(p *player.Player, game *conf.Room_Game) (int32, string) {
	money, vip := p.GetAllMoney(), p.GetVipGrade()
	if money < game.MinMoney {
		return codes.MoneyBelowMinLimit, "MONEY_BELOW_MIN_LIMIT"
	}
	if game.MaxMoney != -1 && money > game.MaxMoney {
		return codes.MoneyOverMaxLimit, "MONEY_OVER_MAX_LIMIT"
	}
	if money < game.BaseMoney {
		return codes.MoneyBelowBaseLimit, "MONEY_BELOW_BASE_LIMIT"
	}
	if vip < game.VipLimit {
		return codes.VIPLimit, "VIP_LIMIT"
	}
	return codes.Success, ""
}

func (m *Manager) table(tableID int32) *Table {
	if tableID <= 0 || tableID > int32(len(m.tables)) {
		return nil
	}
	return m.tables[tableID-1]
}

func (m *Manager) tryAvailableTables(excludedTableID int32, tryTable func(*Table) (bool, error)) (bool, error) {
	for phase := range 2 {
		preferFewPlayers := phase == 0
		for _, gameTable := range m.tables {
			if gameTable == nil || gameTable.ID == excludedTableID || gameTable.isFull() {
				continue
			}
			if (gameTable.seatedCount() <= 1) != preferFewPlayers {
				continue
			}
			done, err := tryTable(gameTable)
			if err != nil || done {
				return done, err
			}
		}
	}
	return false, nil
}

func (m *Manager) hasAvailableTable(excludedTableID int32) bool {
	for _, gameTable := range m.tables {
		if gameTable != nil && gameTable.ID != excludedTableID && !gameTable.isFull() {
			return true
		}
	}
	return false
}

func (m *Manager) call(ctx context.Context, tableID int32, action func(*Table) error) error {
	gameTable := m.table(tableID)
	if gameTable == nil {
		return ErrTableNotFound
	}
	if action == nil {
		return errors.New("table call action is nil")
	}
	executor := m.mailboxes.Executor(int(tableID) - 1)
	return executor.Call(ctx, func() error { return action(gameTable) })
}

// migrationCall waits for temporary queue pressure after removing the player
// from its source table. Once started, it must finish before rollback.
func (m *Manager) migrationCall(ctx context.Context, tableID int32, action func(*Table) error) error {
	gameTable := m.table(tableID)
	if gameTable == nil {
		return ErrTableNotFound
	}
	if action == nil {
		return errors.New("table migration call action is nil")
	}
	return m.mailboxes.PostAndWait(ctx, int(tableID)-1, func() error { return action(gameTable) })
}

func (m *Manager) post(ctx context.Context, tableID int32, action func(*Table)) error {
	gameTable := m.table(tableID)
	if gameTable == nil {
		return ErrTableNotFound
	}
	if action == nil {
		return errors.New("table post action is nil")
	}
	return m.mailboxes.Post(ctx, int(tableID)-1, func() { action(gameTable) })
}

func (m *Manager) tryPost(tableID int32, action func(*Table)) error {
	gameTable := m.table(tableID)
	if gameTable == nil {
		return ErrTableNotFound
	}
	if action == nil {
		return errors.New("table post action is nil")
	}
	executor := m.mailboxes.Executor(int(tableID) - 1)
	return executor.TryPost(func() { action(gameTable) })
}

func (m *Manager) after(tableID int32, delay time.Duration, action func(*Table)) int64 {
	if m.table(tableID) == nil || action == nil {
		return -1
	}
	taskID, err := m.timers.Once(delay, func() {
		err := m.post(m.runCtx, tableID, action)
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, mailbox.ErrStopped) {
			slog.Error("deliver table timer", "table_id", tableID, "error", err)
		}
	})
	if err != nil {
		if !errors.Is(err, timer.ErrStopped) {
			slog.Error("schedule table timer", "table_id", tableID, "error", err)
		}
		return -1
	}
	return int64(taskID)
}

func (m *Manager) cancelTimer(timerID int64) {
	if timerID > 0 {
		m.timers.Cancel(timer.TaskID(timerID))
	}
}

func (m *Manager) pushToUID(uid int64, command int32, msg proto.Message) (bool, error) {
	m.pusherMu.RLock()
	pusher := m.pusher
	m.pusherMu.RUnlock()
	if pusher == nil {
		return false, nil
	}
	return true, pusher.PushToUID(
		context.Background(),
		strconv.FormatInt(uid, 10),
		command,
		msg,
	)
}

func (m *Manager) restorePlayer(gameTable *Table, p *player.Player) error {
	restoreCtx, cancelRestore := context.WithTimeout(context.Background(), tableMigrationTimeout)
	defer cancelRestore()
	restored := false
	if err := m.migrationCall(restoreCtx, gameTable.ID, func(current *Table) error {
		var seatErr error
		restored, seatErr = seatMigratingPlayer(current, p)
		return seatErr
	}); err != nil {
		p.SetTableID(player.TableIDDetached)
		return err
	}
	if !restored {
		p.SetTableID(player.TableIDDetached)
		return errors.New("restore player to original table")
	}
	return nil
}

func seatMigratingPlayer(gameTable *Table, p *player.Player) (bool, error) {
	if !gameTable.Seat(p) {
		return false, nil
	}
	if !p.IsOffline() {
		return true, nil
	}
	gameTable.OnOffline(p)
	return false, ErrPlayerDisconnected
}
