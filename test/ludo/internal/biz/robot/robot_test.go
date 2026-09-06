package robot

import (
	"context"
	"testing"
	"time"

	"yola/test/ludo/internal/biz/player"
	"yola/test/ludo/internal/conf"
)

type repoStub struct{}

func (repo repoStub) CreateRobot(*player.Raw) (*player.Player, error) {
	return nil, nil
}

func (repo repoStub) EnterRobots(context.Context, []*player.Player) ([]int64, error) {
	return nil, nil
}

type loginRepoStub struct {
	repoStub
	players   []*player.Player
	enteredID int64
	enterErr  error
}

func (repo *loginRepoStub) EnterRobots(_ context.Context, players []*player.Player) ([]int64, error) {
	repo.players = append([]*player.Player(nil), players...)
	if len(players) == 0 {
		return nil, nil
	}
	repo.enteredID = players[0].GetPlayerID()
	return []int64{repo.enteredID}, repo.enterErr
}

func TestResetKeepsMoneyWithinRobotRangeWhenRoomMaximumIsUnlimited(t *testing.T) {
	manager := &Manager{rc: &conf.Room{
		Game:  &conf.Room_Game{MinMoney: 100, MaxMoney: -1},
		Robot: &conf.Room_Robot{MinMoney: 100, MaxMoney: 1000},
	}}
	p := player.New(&player.Raw{ID: 1, IsRobot: true, BaseData: &player.BaseData{UID: 1, Money: 500}})

	manager.reset(p)

	if got := p.GetAllMoney(); got != 500 {
		t.Fatalf("reset money = %v, want unchanged 500", got)
	}
}

func TestRunStopsWithContext(t *testing.T) {
	manager := NewManager(&conf.Room{}, repoStub{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		manager.Run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not stop after context cancellation")
	}
}

func TestLoginReleasesRobotsEnteredBeforeTableError(t *testing.T) {
	repo := &loginRepoStub{enterErr: context.Canceled}
	manager := NewManager(&conf.Room{
		Game:  &conf.Room_Game{MinMoney: 100, MaxMoney: 1000, BaseMoney: 100},
		Robot: &conf.Room_Robot{Open: true, MinPlayCount: 1},
	}, repo)
	for _, uid := range []int64{1, 2} {
		p := player.New(&player.Raw{
			ID:       uid,
			IsRobot:  true,
			BaseData: &player.BaseData{UID: uid, Money: 500},
		})
		manager.all.Store(uid, p)
		manager.free.Store(uid, p)
	}

	manager.login(context.Background())

	if len(repo.players) != 2 {
		t.Fatalf("EnterRobots() players = %d, want 2", len(repo.players))
	}
	if _, free := manager.free.Load(repo.enteredID); free {
		t.Fatalf("entered robot %d remained in free pool", repo.enteredID)
	}
	if manager.countFree() != 1 {
		t.Fatalf("free robots = %d, want 1", manager.countFree())
	}
}
