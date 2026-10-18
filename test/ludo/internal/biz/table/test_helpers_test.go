package table

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"yola/test/internal/timer"
	"yola/test/ludo/internal/biz/player"
	"yola/test/ludo/internal/conf"
)

type tableRepoStub struct {
	logoutCount int
}

var _ Repo = (*tableRepoStub)(nil)

func (repo *tableRepoStub) LogoutGame(*player.Player, int32, string) error {
	repo.logoutCount++
	return nil
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

func receiveWithin[T any](t *testing.T, values <-chan T) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for table test synchronization")
		var zero T
		return zero
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
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := tables.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return tables
}

func seatForTest(gameTable *Table, p *player.Player) bool {
	seated, err := gameTable.Seat(p)
	return err == nil && seated
}
