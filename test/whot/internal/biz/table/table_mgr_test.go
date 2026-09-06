package table

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"yola/test/internal/mailbox"
	"yola/test/whot/internal/biz/player"
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
				room := testRoomConfig()
				room.Table.TableNum = tc.tableCount
				manager := NewManager(room, new(managerTestRepo))
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

func TestManagerCloseCanRetryAfterTimeout(t *testing.T) {
	room := testRoomConfig()
	manager := newManager(room, new(managerTestRepo), 1, 1, 1)
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	if err := manager.mailboxes.Executor(0).TryPost(func() {
		close(started)
		<-release
	}); err != nil {
		t.Fatal(err)
	}
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := manager.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close() error = %v, want %v", err, context.DeadlineExceeded)
	}
	if err := manager.mailboxes.Executor(0).TryPost(func() {}); !errors.Is(err, mailbox.ErrStopped) {
		t.Fatalf("TryPost after Close() error = %v, want %v", err, mailbox.ErrStopped)
	}

	close(release)
	if err := manager.Close(context.Background()); err != nil {
		t.Fatalf("retry Close() error = %v", err)
	}
}

func TestManagerCallPlayerRechecksCapturedRoute(t *testing.T) {
	room := testRoomConfig()
	room.Table.TableNum = 2
	manager := newManager(room, new(managerTestRepo), 1, 4, 1)
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

	started := make(chan struct{})
	release := make(chan struct{})
	if err := manager.mailboxes.Executor(0).TryPost(func() {
		close(started)
		<-release
	}); err != nil {
		t.Fatal(err)
	}
	<-started
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
	close(release)
	if err := <-callDone; !errors.Is(err, ErrPlayerRouteChanged) {
		t.Fatalf("CallPlayer() error = %v, want %v", err, ErrPlayerRouteChanged)
	}
	select {
	case <-actionRan:
		t.Fatal("action ran after player route changed")
	default:
	}
}

func TestManagerSwitchMovesPlayerAcrossTableMailboxes(t *testing.T) {
	room := testRoomConfig()
	room.Table.TableNum = 2
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

func TestManagerTableHasStrictBounds(t *testing.T) {
	room := testRoomConfig()
	room.Table.TableNum = 2
	manager := NewManager(room, new(managerTestRepo))
	defer func() { _ = manager.Close(context.Background()) }()

	if manager.table(-1) != nil || manager.table(0) != nil || manager.table(3) != nil {
		t.Fatal("table accepted an ID outside the strict 1-based range")
	}
	if manager.table(1) == nil || manager.table(2) == nil {
		t.Fatal("table rejected a valid 1-based ID")
	}
}

type managerTestRepo struct{}

func (*managerTestRepo) LogoutGame(*player.Player, int32, string) error { return nil }

var _ Repo = (*managerTestRepo)(nil)
