package robot

import (
	"context"
	"testing"
	"time"

	"yola/test/whot/internal/biz/player"
	"yola/test/whot/internal/conf"
)

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
	manager := NewManager(new(conf.Room), new(startRepo))
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

type startRepo struct{}

func (*startRepo) CreateRobot(*player.Raw) (*player.Player, error) {
	return nil, nil
}
func (*startRepo) EnterRobots(context.Context, []*player.Player) ([]int64, error) { return nil, nil }

var _ Repo = (*startRepo)(nil)
