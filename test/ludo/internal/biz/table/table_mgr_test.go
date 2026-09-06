package table

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"yola/test/internal/mailbox"
	"yola/test/internal/timer"
	"yola/test/ludo/internal/biz/player"
	"yola/test/ludo/internal/conf"
	"yola/test/ludo/pkg/codes"
)

func TestManagerLimitsConcurrentTableJobs(t *testing.T) {
	for _, tc := range []struct {
		name       string
		tableCount int32
		workers    int
	}{
		{name: "one_table", tableCount: 1, workers: 1},
		{name: "below_limit", tableCount: 15, workers: 15},
		{name: "at_limit", tableCount: 16, workers: 16},
		{name: "above_limit", tableCount: 17, workers: 16},
		{name: "thousand_tables", tableCount: 1000, workers: 16},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				manager := NewManager("ludo-test", testRoom(tc.tableCount), new(tableRepoStub))
				started := make(chan struct{}, tc.tableCount)
				release := make(chan struct{})
				t.Cleanup(func() {
					close(release)
					if err := manager.Close(context.Background()); err != nil {
						t.Error(err)
					}
				})
				if err := manager.Start(context.Background()); err != nil {
					t.Fatal(err)
				}
				for index := range manager.tables {
					if err := manager.mailboxes.Executor(index).TryPost(func() {
						started <- struct{}{}
						<-release
					}); err != nil {
						t.Fatal(err)
					}
				}
				synctest.Wait()
				if got := len(started); got != tc.workers {
					t.Fatalf("concurrent table jobs = %d, want %d", got, tc.workers)
				}
			})
		})
	}
}

func TestManagerUsesStrictTableIDBoundaries(t *testing.T) {
	room := testRoom(2)
	room.LogCache.Open = false
	tables := newStartedManager(t, room, new(tableRepoStub))

	for _, id := range []int32{-1, 0, 3} {
		if gameTable := tables.table(id); gameTable != nil {
			t.Fatalf("table(%d) = table %d, want nil", id, gameTable.ID)
		}
	}
	if first := tables.table(1); first == nil || first.ID != 1 {
		t.Fatalf("table(1) = %v", first)
	}
	if second := tables.table(2); second == nil || second.ID != 2 {
		t.Fatalf("table(2) = %v", second)
	}
}

func BenchmarkTryAvailableTablesFull(b *testing.B) {
	for _, tableCount := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("tables=%d", tableCount), func(b *testing.B) {
			manager := &Manager{tables: make([]*Table, tableCount)}
			for index := range manager.tables {
				gameTable := &Table{ID: int32(index + 1), maxPlayers: 1}
				gameTable.seatCount.Store(1)
				manager.tables[index] = gameTable
			}
			unexpected := func(*Table) (bool, error) {
				b.Fatal("full table reached selection callback")
				return false, nil
			}

			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				entered, err := manager.tryAvailableTables(0, unexpected)
				if err != nil || entered {
					b.Fatalf("tryAvailableTables() = (%v, %v), want (false, nil)", entered, err)
				}
			}
		})
	}
}

func TestManagerCloseCanRetryAfterTimeout(t *testing.T) {
	room := testRoom(1)
	room.LogCache.Open = false
	tables := newManager("ludo-test", room, new(tableRepoStub), 1, 1, 1)
	if err := tables.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	if err := tables.mailboxes.Executor(0).TryPost(func() {
		close(started)
		<-release
	}); err != nil {
		t.Fatal(err)
	}
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := tables.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close() error = %v, want %v", err, context.DeadlineExceeded)
	}
	if err := tables.mailboxes.Executor(0).TryPost(func() {}); !errors.Is(err, mailbox.ErrStopped) {
		t.Fatalf("TryPost after Close() error = %v, want %v", err, mailbox.ErrStopped)
	}

	close(release)
	if err := tables.Close(context.Background()); err != nil {
		t.Fatalf("retry Close() error = %v", err)
	}
}

func TestManagerEnterTriesNextTableWhenFirstMailboxIsFull(t *testing.T) {
	room := testRoom(2)
	room.Table.ChairNum = 1
	room.LogCache.Open = false
	tables := newStartedManagerWithConfig(t, room, new(tableRepoStub), 2, 1, 1)

	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	first := tables.mailboxes.Executor(0)
	if err := first.TryPost(func() {
		close(started)
		<-release
	}); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := first.TryPost(func() {}); err != nil {
		t.Fatal(err)
	}

	p := player.New(&player.Raw{ID: 1, BaseData: &player.BaseData{UID: 1, Money: 500}})
	code, _, err := tables.Enter(context.Background(), p, 0)
	if err != nil || code != codes.Success {
		t.Fatalf("Enter() = (%d, %v), want success", code, err)
	}
	if p.GetTableID() != 2 || tables.table(2).playerAt(0) != p {
		t.Fatalf("player route = %d, want table 2", p.GetTableID())
	}
}

func TestManagerSeatsRobotsAcrossTables(t *testing.T) {
	room := testRoom(2)
	room.LogCache.Open = false
	room.Robot = &conf.Room_Robot{Open: true, TableMaxCount: 1, MinPlayCount: 2}
	tables := newStartedManager(t, room, new(tableRepoStub))
	players := []*player.Player{
		newRobotPlayer(1),
		newRobotPlayer(2),
		newRobotPlayer(3),
	}

	entered, err := tables.EnterRobots(context.Background(), players)
	if err != nil {
		t.Fatal(err)
	}
	if len(entered) != 2 || entered[0] != 1 || entered[1] != 2 {
		t.Fatalf("EnterRobots() = %v, want [1 2]", entered)
	}
	if players[0].GetTableID() != 1 || players[1].GetTableID() != 2 || players[2].GetTableID() > 0 {
		t.Fatalf("robot tables = (%d,%d,%d), want (1,2,unseated)", players[0].GetTableID(), players[1].GetTableID(), players[2].GetTableID())
	}
}

func TestManagerCallPlayerRechecksCapturedRoute(t *testing.T) {
	room := testRoom(2)
	room.LogCache.Open = false
	tables := newStartedManagerWithConfig(t, room, new(tableRepoStub), 1, 1, 1)
	mailboxes := tables.mailboxes

	p := player.New(&player.Raw{ID: 1, BaseData: &player.BaseData{UID: 1, Money: 500}})
	if !seatForTest(tables.table(1), p) {
		t.Fatal("seat player at first table")
	}
	started := make(chan struct{})
	release := make(chan struct{})
	if err := mailboxes.Executor(0).TryPost(func() { close(started); <-release }); err != nil {
		t.Fatal(err)
	}
	<-started

	called := make(chan struct{}, 1)
	result := make(chan error, 1)
	go func() {
		result <- tables.CallPlayer(context.Background(), p, func(*Table) error {
			called <- struct{}{}
			return nil
		})
	}()
	waitTableTest(t, func() bool { return mailboxes.Executor(0).Stats().Free == 0 })
	p.SetTableID(2)
	close(release)
	if callErr := <-result; !errors.Is(callErr, ErrPlayerRouteChanged) {
		t.Fatalf("CallPlayer error = %v, want %v", callErr, ErrPlayerRouteChanged)
	}
	select {
	case <-called:
		t.Fatal("stale table call executed")
	default:
	}
}

func TestTableIgnoresStaleStageTimer(t *testing.T) {
	room := testRoom(1)
	room.LogCache.Open = false
	tables := newStartedManager(t, room, new(tableRepoStub))
	useManualTimers(t, tables)

	var staleTimerID, currentTimerID int64
	var staleDone <-chan struct{}
	if err := tables.call(context.Background(), 1, func(gameTable *Table) error {
		gameTable.enterStageFor(StResult, time.Hour)
		staleTimerID = gameTable.stage.TimerID()
		started, done, ok := startTableTimer(tables, staleTimerID)
		if !ok {
			return fmt.Errorf("start stale timer %d", staleTimerID)
		}
		staleDone = done
		<-started
		gameTable.enterStageFor(StReady, time.Hour)
		currentTimerID = gameTable.stage.TimerID()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if staleTimerID == currentTimerID || staleTimerID <= 0 || currentTimerID <= 0 {
		t.Fatalf("stage timer IDs = (%d, %d), want distinct positive IDs", staleTimerID, currentTimerID)
	}
	select {
	case <-staleDone:
	case <-time.After(time.Second):
		t.Fatalf("stale timer %d did not finish", staleTimerID)
	}
	if err := tables.call(context.Background(), 1, func(gameTable *Table) error {
		if state := gameTable.stage.State(); state != StReady {
			return fmt.Errorf("stage after stale timer = %s, want %s", state, StReady)
		}
		if timerID := gameTable.stage.TimerID(); timerID != currentTimerID {
			return fmt.Errorf("timer ID after stale timer = %d, want %d", timerID, currentTimerID)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestManagerSwitchTriesNextCandidateAfterConcurrentFill(t *testing.T) {
	room := testRoom(3)
	room.Table.ChairNum = 1
	room.LogCache.Open = false
	tables := newStartedManagerWithConfig(t, room, new(tableRepoStub), 3, 4, 1)
	mailboxes := tables.mailboxes

	p := player.New(&player.Raw{ID: 1, BaseData: &player.BaseData{UID: 1, Money: 500}})
	other := player.New(&player.Raw{ID: 2, BaseData: &player.BaseData{UID: 2, Money: 500}})
	if !seatForTest(tables.table(1), p) {
		t.Fatal("seat switching player")
	}
	started := make(chan struct{})
	release := make(chan struct{})
	if err := mailboxes.Executor(1).TryPost(func() { close(started); <-release }); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := mailboxes.Executor(1).TryPost(func() { seatForTest(tables.table(2), other) }); err != nil {
		t.Fatal(err)
	}

	result := make(chan error, 1)
	go func() {
		code, _, switchErr := tables.Switch(context.Background(), p, room.Game)
		if switchErr == nil && code != codes.Success {
			switchErr = fmt.Errorf("switch code = %d, want %d", code, codes.Success)
		}
		result <- switchErr
	}()
	waitTableTest(t, func() bool { return p.GetTableID() == player.TableIDPending })
	close(release)
	if switchErr := <-result; switchErr != nil {
		t.Fatal(switchErr)
	}
	if tableID := p.GetTableID(); tableID != 3 {
		t.Fatalf("table after candidate collision = %d, want 3", tableID)
	}
}

func TestManagerSwitchRestoresPlayerWhenTargetFills(t *testing.T) {
	room := testRoom(2)
	room.Table.ChairNum = 1
	tables := newStartedManagerWithConfig(t, room, new(tableRepoStub), 2, 4, 1)
	p := player.New(&player.Raw{ID: 1, BaseData: &player.BaseData{UID: 1, Money: 500}})
	other := player.New(&player.Raw{ID: 2, BaseData: &player.BaseData{UID: 2, Money: 500}})
	if !seatForTest(tables.table(1), p) {
		t.Fatal("seat switching player")
	}

	started := make(chan struct{})
	release := make(chan struct{})
	target := tables.mailboxes.Executor(1)
	if err := target.TryPost(func() { close(started); <-release }); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := target.TryPost(func() { seatForTest(tables.table(2), other) }); err != nil {
		t.Fatal(err)
	}

	result := make(chan struct {
		code int32
		err  error
	}, 1)
	go func() {
		code, _, err := tables.Switch(context.Background(), p, room.Game)
		result <- struct {
			code int32
			err  error
		}{code: code, err: err}
	}()
	waitTableTest(t, func() bool { return p.GetTableID() == player.TableIDPending })
	close(release)
	got := <-result
	if got.err != nil || got.code != codes.EnterTableFail {
		t.Fatalf("Switch() = (%d, %v), want ENTER_TABLE_FAIL", got.code, got.err)
	}
	if p.GetTableID() != 1 || tables.table(1).playerAt(p.GetChairID()) != p {
		t.Fatalf("player was not restored to table 1: %s", p.Desc())
	}
}

func TestManagerSwitchFinishesMigrationAfterRequestCancellation(t *testing.T) {
	room := testRoom(2)
	room.Table.ChairNum = 1
	room.LogCache.Open = false
	tables := newStartedManagerWithConfig(t, room, new(tableRepoStub), 2, 4, 1)
	mailboxes := tables.mailboxes

	p := player.New(&player.Raw{ID: 1, BaseData: &player.BaseData{UID: 1, Money: 500}})
	if !seatForTest(tables.table(1), p) {
		t.Fatal("seat switching player")
	}
	started := make(chan struct{})
	release := make(chan struct{})
	if err := mailboxes.Executor(1).TryPost(func() { close(started); <-release }); err != nil {
		t.Fatal(err)
	}
	<-started

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		code, _, switchErr := tables.Switch(ctx, p, room.Game)
		if switchErr == nil && code != codes.Success {
			switchErr = fmt.Errorf("switch code = %d, want %d", code, codes.Success)
		}
		result <- switchErr
	}()
	waitTableTest(t, func() bool { return p.GetTableID() == player.TableIDPending })
	cancel()
	select {
	case switchErr := <-result:
		t.Fatalf("migration stopped after request cancellation: %v", switchErr)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if switchErr := <-result; switchErr != nil {
		t.Fatal(switchErr)
	}
	if tableID := p.GetTableID(); tableID != 2 {
		t.Fatalf("table after canceled migration = %d, want 2", tableID)
	}
}

func TestManagerSwitchMarksPlayerDetachedWhenOriginalTableIsOccupied(t *testing.T) {
	room := testRoom(2)
	room.Table.ChairNum = 1
	room.LogCache.Open = false
	tables := newStartedManagerWithConfig(t, room, new(tableRepoStub), 2, 2, 1)
	p := player.New(&player.Raw{ID: 1, BaseData: &player.BaseData{UID: 1, Money: 500}})
	oldOccupant := player.New(&player.Raw{ID: 2, BaseData: &player.BaseData{UID: 2, Money: 500}})
	targetOccupant := player.New(&player.Raw{ID: 3, BaseData: &player.BaseData{UID: 3, Money: 500}})
	if !seatForTest(tables.table(1), p) {
		t.Fatal("seat switching player")
	}

	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	target := tables.mailboxes.Executor(1)
	if err := target.TryPost(func() {
		close(started)
		<-release
	}); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := target.TryPost(func() { seatForTest(tables.table(2), targetOccupant) }); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, _, err := tables.Switch(context.Background(), p, room.Game)
		done <- err
	}()
	waitTableTest(t, func() bool { return p.GetTableID() == player.TableIDPending })
	if err := tables.call(context.Background(), 1, func(gameTable *Table) error {
		if !seatForTest(gameTable, oldOccupant) {
			return errors.New("occupy original table")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	unblock()
	if err := <-done; err == nil {
		t.Fatal("Switch() error = nil, want restore failure")
	}
	if p.GetTableID() != player.TableIDDetached {
		t.Fatalf("player table = %d, want detached", p.GetTableID())
	}
}

func testRoom(tableCount int32) *conf.Room {
	return &conf.Room{
		Table:    &conf.Room_Table{TableNum: tableCount, ChairNum: 4},
		Game:     &conf.Room_Game{MinMoney: 100, MaxMoney: 1000, BaseMoney: 100},
		Robot:    &conf.Room_Robot{},
		LogCache: &conf.Room_LogCache{},
	}
}

func newRobotPlayer(uid int64) *player.Player {
	return player.New(&player.Raw{
		ID:       uid,
		IsRobot:  true,
		BaseData: &player.BaseData{UID: uid, Money: 500},
	})
}

func waitTableTest(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition was not satisfied")
		}
		time.Sleep(time.Millisecond)
	}
}

func fireTableTimer(t *testing.T, tables *Manager, timerID int64) bool {
	t.Helper()
	return testTimers(t, tables).fire(timerID)
}

func startTableTimer(tables *Manager, timerID int64) (<-chan struct{}, <-chan struct{}, bool) {
	timers, ok := tables.timers.(*manualScheduler)
	if !ok {
		return nil, nil, false
	}
	return timers.start(timerID)
}

func latestTableTimerID(tables *Manager) int64 {
	timers, ok := tables.timers.(*manualScheduler)
	if !ok {
		return -1
	}
	return timers.latestID()
}

type manualScheduler struct {
	mu      sync.Mutex
	nextID  timer.TaskID
	jobs    map[timer.TaskID]func()
	started bool
	closed  bool
}

func (timers *manualScheduler) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("start manual timer: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	timers.mu.Lock()
	defer timers.mu.Unlock()
	if timers.closed {
		return timer.ErrStopped
	}
	if timers.started {
		return timer.ErrStarted
	}
	timers.started = true
	return nil
}

func (timers *manualScheduler) Stop(ctx context.Context) error {
	if ctx == nil {
		return errors.New("stop manual timer: context is nil")
	}
	timers.mu.Lock()
	timers.closed = true
	clear(timers.jobs)
	timers.mu.Unlock()
	return nil
}

func (timers *manualScheduler) Once(delay time.Duration, callback func()) (timer.TaskID, error) {
	if delay < 0 {
		return 0, errors.New("manual timer delay is negative")
	}
	if callback == nil {
		return 0, errors.New("manual timer callback is nil")
	}
	timers.mu.Lock()
	defer timers.mu.Unlock()
	if timers.closed {
		return 0, timer.ErrStopped
	}
	if timers.jobs == nil {
		timers.jobs = make(map[timer.TaskID]func())
	}
	timers.nextID++
	timers.jobs[timers.nextID] = callback
	return timers.nextID, nil
}

func (timers *manualScheduler) Forever(interval time.Duration, callback func()) (timer.TaskID, error) {
	if interval <= 0 {
		return 0, errors.New("manual timer interval must be positive")
	}
	return timers.Once(interval, callback)
}

func (timers *manualScheduler) ForeverNow(interval time.Duration, callback func()) (timer.TaskID, error) {
	if interval <= 0 {
		return 0, errors.New("manual timer interval must be positive")
	}
	return timers.Once(0, callback)
}

func (timers *manualScheduler) Cancel(timerID timer.TaskID) bool {
	timers.mu.Lock()
	_, found := timers.jobs[timerID]
	delete(timers.jobs, timerID)
	timers.mu.Unlock()
	return found
}

func (timers *manualScheduler) CancelAll() {
	timers.mu.Lock()
	clear(timers.jobs)
	timers.mu.Unlock()
}

func (timers *manualScheduler) Monitor() timer.Monitor {
	timers.mu.Lock()
	defer timers.mu.Unlock()
	return timer.Monitor{Total: len(timers.jobs)}
}

func (timers *manualScheduler) fire(timerID int64) bool {
	callback := timers.take(timer.TaskID(timerID))
	if callback == nil {
		return false
	}
	callback()
	return true
}

func (timers *manualScheduler) start(timerID int64) (<-chan struct{}, <-chan struct{}, bool) {
	callback := timers.take(timer.TaskID(timerID))
	if callback == nil {
		return nil, nil, false
	}
	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		close(started)
		callback()
		close(done)
	}()
	return started, done, true
}

func (timers *manualScheduler) take(timerID timer.TaskID) func() {
	timers.mu.Lock()
	defer timers.mu.Unlock()
	callback := timers.jobs[timerID]
	delete(timers.jobs, timerID)
	return callback
}

func (timers *manualScheduler) latestID() int64 {
	timers.mu.Lock()
	defer timers.mu.Unlock()
	return int64(timers.nextID)
}

func useManualTimers(t *testing.T, tables *Manager) {
	t.Helper()
	if err := tables.timers.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	timers := new(manualScheduler)
	if err := timers.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	tables.timers = timers
}

func testTimers(t *testing.T, tables *Manager) *manualScheduler {
	t.Helper()
	timers, ok := tables.timers.(*manualScheduler)
	if !ok {
		t.Fatal("table test does not use manual timers")
	}
	return timers
}

func newStartedManager(t *testing.T, room *conf.Room, repo Repo) *Manager {
	t.Helper()
	return newStartedManagerWithConfig(t, room, repo, 1, defaultTableQueueSize, defaultTableMailboxBatch)
}

func newStartedManagerWithConfig(t *testing.T, room *conf.Room, repo Repo, workers, queueSize, batchSize int) *Manager {
	t.Helper()
	tables := newManager("ludo-test", room, repo, workers, queueSize, batchSize)
	if err := tables.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = tables.Close(context.Background())
	})
	return tables
}
