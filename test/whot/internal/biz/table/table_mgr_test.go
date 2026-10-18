package table

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"yola/test/internal/mailbox"
	"yola/test/whot/internal/biz/player"
	"yola/test/whot/internal/conf"
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
				room := &conf.Room{
					Table:    &conf.Room_Table{TableNum: tc.tableCount, ChairNum: 4},
					Game:     &conf.Room_Game{MaxMoney: -1, BaseMoney: 1},
					Robot:    &conf.Room_Robot{},
					LogCache: &conf.Room_LogCache{},
				}
				manager := NewManager(room, new(managerTestRepo))
				started := make(chan struct{}, tc.tableCount)
				release := make(chan struct{})
				t.Cleanup(func() {
					close(release)
					ctx, cancel := context.WithTimeout(context.Background(), time.Second)
					defer cancel()
					if err := manager.Close(ctx); err != nil {
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

func TestManagerCloseCanRetryAfterTimeout(t *testing.T) {
	room := testRoomConfig()
	manager := newManager(room, new(managerTestRepo), 1, 1, 1)
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := manager.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	started := make(chan struct{})
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	if err := manager.mailboxes.Executor(0).TryPost(func() {
		close(started)
		<-release
	}); err != nil {
		t.Fatal(err)
	}
	receiveWithin(t, started)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := manager.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close() error = %v, want %v", err, context.DeadlineExceeded)
	}
	if err := manager.mailboxes.Executor(0).TryPost(func() {}); !errors.Is(err, mailbox.ErrStopped) {
		t.Fatalf("TryPost after Close() error = %v, want %v", err, mailbox.ErrStopped)
	}

	unblock()
	retryCtx, retryCancel := context.WithTimeout(context.Background(), time.Second)
	defer retryCancel()
	if err := manager.Close(retryCtx); err != nil {
		t.Fatalf("retry Close() error = %v", err)
	}
}

func TestManagerCallPlayerRechecksCapturedRoute(t *testing.T) {
	room := &conf.Room{
		Table:    &conf.Room_Table{TableNum: 2, ChairNum: 4},
		Game:     &conf.Room_Game{MaxMoney: -1, BaseMoney: 1},
		Robot:    &conf.Room_Robot{},
		LogCache: &conf.Room_LogCache{},
	}
	manager := newManager(room, new(managerTestRepo), 1, 4, 1)
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := manager.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	p := player.New(&player.Raw{ID: 42, BaseData: &player.BaseData{UID: 42, Money: 100}})
	if err := manager.call(context.Background(), 1, func(gameTable *Table) error {
		if !gameTable.Seat(p) {
			return errors.New("seat player")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	if err := manager.mailboxes.Executor(0).TryPost(func() {
		close(started)
		<-release
	}); err != nil {
		t.Fatal(err)
	}
	receiveWithin(t, started)
	actionRan := make(chan struct{})
	callDone := make(chan error, 1)
	go func() {
		callDone <- manager.CallPlayer(context.Background(), p, func(*Table) error {
			close(actionRan)
			return nil
		})
	}()
	waitForTableTest(t, func() bool { return manager.mailboxes.Executor(0).Stats().Free == 3 })
	p.SetTableID(2)
	unblock()
	if err := receiveWithin(t, callDone); !errors.Is(err, ErrPlayerRouteChanged) {
		t.Fatalf("CallPlayer() error = %v, want %v", err, ErrPlayerRouteChanged)
	}
	select {
	case <-actionRan:
		t.Fatal("action ran after player route changed")
	default:
	}
}

func TestManagerSwitchMovesPlayerAcrossTableMailboxes(t *testing.T) {
	room := &conf.Room{
		Table:    &conf.Room_Table{TableNum: 2, ChairNum: 4},
		Game:     &conf.Room_Game{MaxMoney: -1, BaseMoney: 1},
		Robot:    &conf.Room_Robot{},
		LogCache: &conf.Room_LogCache{},
	}
	manager := newManager(room, new(managerTestRepo), 2, 4, 1)
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := manager.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	p := player.New(&player.Raw{ID: 42, BaseData: &player.BaseData{UID: 42, Money: 100}})
	if err := manager.call(context.Background(), 1, func(gameTable *Table) error {
		if !gameTable.Seat(p) {
			return errors.New("seat player")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	code, message, err := manager.Switch(context.Background(), p, room.Game)
	if err != nil || code != 0 {
		t.Fatalf("Switch() = (%d, %q, %v)", code, message, err)
	}
	if p.GetTableID() != 2 || manager.table(1).seatedCount() != 0 || manager.table(2).playerAt(p.GetChairID()) != p {
		t.Fatalf("player route after switch = table %d chair %d", p.GetTableID(), p.GetChairID())
	}
}

func TestManagerTableHasStrictBounds(t *testing.T) {
	room := &conf.Room{
		Table:    &conf.Room_Table{TableNum: 2, ChairNum: 4},
		Game:     &conf.Room_Game{MaxMoney: -1, BaseMoney: 1},
		Robot:    &conf.Room_Robot{},
		LogCache: &conf.Room_LogCache{},
	}
	manager := NewManager(room, new(managerTestRepo))
	defer func() { _ = manager.Close(context.Background()) }()

	if manager.table(-1) != nil || manager.table(0) != nil || manager.table(3) != nil {
		t.Fatal("table accepted an ID outside the strict 1-based range")
	}
	if manager.table(1) == nil || manager.table(2) == nil {
		t.Fatal("table rejected a valid 1-based ID")
	}
}

func waitForTableTest(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for table condition")
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

var _ Repo = (*managerTestRepo)(nil)

type managerTestRepo struct{}

func (*managerTestRepo) LogoutGame(*player.Player, int32, string) error { return nil }
