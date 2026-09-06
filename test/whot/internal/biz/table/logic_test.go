package table

import (
	"context"
	"testing"
	"time"

	"yola/test/internal/timer"
	"yola/test/whot/api/v1"
	"yola/test/whot/internal/biz/player"
	"yola/test/whot/internal/conf"
)

func TestRoundRunsReadyPlayResultAndReturnsToWait(t *testing.T) {
	room := &conf.Room{
		Table:    &conf.Room_Table{TableNum: 1, ChairNum: 2},
		Game:     &conf.Room_Game{MaxMoney: -1, BaseMoney: 1},
		Robot:    &conf.Room_Robot{},
		LogCache: &conf.Room_LogCache{},
	}
	timers := new(tableTimerStub)
	manager, gameTable := newRoundTestTable(t, room, timers)
	players := []*player.Player{
		player.New(&player.Raw{ID: 1, BaseData: &player.BaseData{UID: 1, Money: 100}}),
		player.New(&player.Raw{ID: 2, BaseData: &player.BaseData{UID: 2, Money: 100}}),
	}
	for _, p := range players {
		if !gameTable.Seat(p) || !gameTable.OnReadyReq(p, true) {
			t.Fatalf("ready player %d", p.GetPlayerID())
		}
	}
	assertWhotStage(t, gameTable, StReady)
	if !fireTableTimer(t, manager, timers, gameTable.stage.TimerID()) {
		t.Fatal("ready timer is missing")
	}
	assertWhotStage(t, gameTable, StSendCard)
	for _, p := range players {
		if !p.IsGaming() || len(p.GetCards()) != 5 {
			t.Fatalf("player %d game state: gaming=%v cards=%v", p.GetPlayerID(), p.IsGaming(), p.GetCards())
		}
	}
	if !fireTableTimer(t, manager, timers, gameTable.stage.TimerID()) {
		t.Fatal("send-card timer is missing")
	}
	assertWhotStage(t, gameTable, StPlaying)

	active := gameTable.GetActivePlayer()
	if active == nil || len(active.GetCards()) == 0 {
		t.Fatal("active player or hand is missing")
	}
	card := active.GetCards()[0]
	for _, other := range append([]int32(nil), active.GetCards()[1:]...) {
		active.RemoveCard(other)
	}
	gameTable.currCard = card
	gameTable.pending = nil
	if !gameTable.OnPlayerActionReq(active, &v1.PlayerActionReq{
		UserId:  active.GetPlayerID(),
		Action:  v1.ACTION_PLAY_CARD,
		OutCard: card,
	}, false) {
		t.Fatal("valid final-card action was rejected")
	}
	assertWhotStage(t, gameTable, StWaitEnd)
	if !fireTableTimer(t, manager, timers, gameTable.stage.TimerID()) {
		t.Fatal("result timer is missing")
	}
	assertWhotStage(t, gameTable, StEnd)
	if !fireTableTimer(t, manager, timers, gameTable.stage.TimerID()) {
		t.Fatal("end timer is missing")
	}
	assertWhotStage(t, gameTable, StWait)
}

func TestTableIgnoresExpiredStageTimerAfterStageChanged(t *testing.T) {
	room := &conf.Room{
		Table:    &conf.Room_Table{TableNum: 1, ChairNum: 2},
		Game:     &conf.Room_Game{MaxMoney: -1, BaseMoney: 1},
		Robot:    &conf.Room_Robot{},
		LogCache: &conf.Room_LogCache{},
	}
	timers := new(tableTimerStub)
	manager, gameTable := newRoundTestTable(t, room, timers)

	gameTable.updateStageWith(StPlaying, time.Hour)
	staleTimerID := gameTable.stage.TimerID()
	gameTable.updateStageWith(StEnd, time.Hour)
	currentTimerID := gameTable.stage.TimerID()
	if !fireTableTimer(t, manager, timers, staleTimerID) {
		t.Fatalf("stale timer %d is missing", staleTimerID)
	}
	if got := gameTable.stage.State(); got != StEnd {
		t.Fatalf("stage after stale timer = %s, want %s", got, StEnd)
	}
	if got := gameTable.stage.TimerID(); got != currentTimerID {
		t.Fatalf("timer after stale callback = %d, want %d", got, currentTimerID)
	}
}

func assertWhotStage(t *testing.T, gameTable *Table, want StageID) {
	t.Helper()
	if got := gameTable.stage.State(); got != want {
		t.Fatalf("stage = %s, want %s", got, want)
	}
}

type roundRepoStub struct{}

func (*roundRepoStub) LogoutGame(*player.Player, int32, string) error { return nil }

type tableTimerStub struct {
	nextID timer.TaskID
	jobs   map[timer.TaskID]func()
}

func (*tableTimerStub) Start(context.Context) error { return nil }
func (*tableTimerStub) Stop(context.Context) error  { return nil }
func (timers *tableTimerStub) Monitor() timer.Monitor {
	return timer.Monitor{Total: len(timers.jobs)}
}

func (timers *tableTimerStub) Once(_ time.Duration, job func()) (timer.TaskID, error) {
	if timers.jobs == nil {
		timers.jobs = make(map[timer.TaskID]func())
	}
	timers.nextID++
	timers.jobs[timers.nextID] = job
	return timers.nextID, nil
}

func (timers *tableTimerStub) Forever(delay time.Duration, job func()) (timer.TaskID, error) {
	return timers.Once(delay, job)
}

func (timers *tableTimerStub) ForeverNow(delay time.Duration, job func()) (timer.TaskID, error) {
	return timers.Once(delay, job)
}
func (*tableTimerStub) Cancel(timer.TaskID) bool { return true }
func (*tableTimerStub) CancelAll()               {}

func (timers *tableTimerStub) fire(id int64) bool {
	job := timers.jobs[timer.TaskID(id)]
	if job == nil {
		return false
	}
	delete(timers.jobs, timer.TaskID(id))
	job()
	return true
}

func newRoundTestTable(t *testing.T, room *conf.Room, timers timer.Scheduler) (*Manager, *Table) {
	t.Helper()
	manager := newManager(room, new(roundRepoStub), 1, 16, 8)
	manager.timers = timers
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := manager.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return manager, manager.table(1)
}

func fireTableTimer(t *testing.T, manager *Manager, timers *tableTimerStub, timerID int64) bool {
	t.Helper()
	if !timers.fire(timerID) {
		return false
	}
	drainTable(t, manager, 1)
	return true
}

var _ Repo = (*roundRepoStub)(nil)
