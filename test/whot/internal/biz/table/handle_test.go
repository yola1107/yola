package table

import (
	"context"
	"slices"
	"testing"
	"time"

	"yola/test/whot/api/v1"
	"yola/test/whot/internal/biz/player"
	"yola/test/whot/internal/conf"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestOnExitGameReportsSuccess(t *testing.T) {
	repo := &exitTestRepo{room: testRoomConfig()}
	_, table := newTestTable(t, repo.room, repo)
	p := player.New(&player.Raw{BaseData: &player.BaseData{UID: 7, Money: 100}})
	if !table.Seat(p) {
		t.Fatal("failed to arrange seated player")
	}
	if err := table.OnExitGame(p, 0, "test"); err != nil {
		t.Fatalf("OnExitGame() error = %v", err)
	}
	if repo.logoutCalls != 1 {
		t.Fatalf("LogoutGame calls = %d, want 1", repo.logoutCalls)
	}
}

func TestDrawPushKeepsPrivateHandInDrawResult(t *testing.T) {
	table, actor, _, actorSession, observerSession := newPushTestTable(t)
	actor.AddCards([]int32{101, 102, 103})
	actorSession.reset()
	observerSession.reset()

	table.broadcastPlayerAction(actor, v1.ACTION_DRAW_CARD, []int32{103}, 0)
	actorPush := actorSession.lastActionPush(t)
	if !slices.Equal(actorPush.DrawResult.Drawn, []int32{103}) {
		t.Fatalf("actor drawn cards = %v, want [103]", actorPush.DrawResult.Drawn)
	}
	if !slices.Equal(actorPush.DrawResult.Cards, actor.GetCards()) {
		t.Fatalf("actor draw hand = %v, want %v", actorPush.DrawResult.Cards, actor.GetCards())
	}
	if len(actorPush.PlayResult.Cards) != 0 {
		t.Fatalf("draw action populated play hand: %v", actorPush.PlayResult.Cards)
	}
	observerPush := observerSession.lastActionPush(t)
	if len(observerPush.DrawResult.Drawn) != 0 || len(observerPush.DrawResult.Cards) != 0 {
		t.Fatalf("observer received private draw data: %+v", observerPush.DrawResult)
	}
}

func TestSceneOnlyIncludesRequestingPlayersPrivateCards(t *testing.T) {
	table, viewer, opponent, _, _ := newPushTestTable(t)
	viewer.AddCards([]int32{101, 201})
	opponent.AddCards([]int32{102, 202})
	viewer.SetGaming()
	opponent.SetGaming()
	table.currCard = 101
	table.active = opponent.GetChairID()
	table.stage.Set(StPlaying, time.Second, 0)

	scene := table.OnSceneReq(viewer)
	viewerInfo := playerInfoByID(t, scene, viewer.GetPlayerID())
	opponentInfo := playerInfoByID(t, scene, opponent.GetPlayerID())
	if !slices.Equal(viewerInfo.Cards, viewer.GetCards()) {
		t.Fatalf("viewer cards = %v, want %v", viewerInfo.Cards, viewer.GetCards())
	}
	if len(opponentInfo.Cards) != 0 || len(opponentInfo.CanOp) != 0 {
		t.Fatalf("scene leaked opponent private state: cards=%v canOp=%v", opponentInfo.Cards, opponentInfo.CanOp)
	}
}

func TestPushNotFoundMarksOfflineAndQueuesNonGamingCleanup(t *testing.T) {
	repo := &exitTestRepo{room: testRoomConfig()}
	manager, table := newTestTable(t, repo.room, repo)
	sess := new(captureSession)
	p := player.New(&player.Raw{
		Session:  sess,
		BaseData: &player.BaseData{UID: 42, Money: 100},
	})
	if !table.Seat(p) {
		t.Fatal("failed to arrange seated player")
	}
	sess.reset()
	sess.pushErr = status.Error(codes.NotFound, "binding not found")

	table.SendPacketToClient(p, v1.GameCommand_CmdActivePush, new(v1.ActivePush))
	if !p.IsOffline() {
		t.Fatal("NotFound push did not mark player offline")
	}
	if got := sess.attempts; got != 1 {
		t.Fatalf("push attempts = %d, want 1", got)
	}

	table.SendPacketToClient(p, v1.GameCommand_CmdActivePush, new(v1.ActivePush))
	if got := sess.attempts; got != 1 {
		t.Fatalf("push attempts after offline = %d, want still 1", got)
	}
	drainTable(t, manager, table.ID)
	if got := repo.logoutCalls; got != 1 {
		t.Fatalf("LogoutGame calls = %d, want 1", got)
	}
	if got := table.seatedCount(); got != 0 {
		t.Fatalf("seated players after cleanup = %d, want 0", got)
	}
}

func TestPushNotFoundCleanupSurvivesFullTableQueue(t *testing.T) {
	repo := &exitTestRepo{room: testRoomConfig()}
	manager := newManager(repo.room, repo, 1, 1, 1)
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := manager.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	gameTable := manager.table(1)
	sess := new(captureSession)
	p := player.New(&player.Raw{Session: sess, BaseData: &player.BaseData{UID: 42, Money: 100}})
	if !gameTable.Seat(p) {
		t.Fatal("failed to arrange seated player")
	}
	sess.reset()
	sess.pushErr = status.Error(codes.NotFound, "binding not found")

	started := make(chan struct{})
	queueFilled := make(chan struct{})
	pushDone := make(chan struct{})
	if err := manager.mailboxes.Executor(0).TryPost(func() {
		close(started)
		<-queueFilled
		gameTable.SendPacketToClient(p, v1.GameCommand_CmdActivePush, new(v1.ActivePush))
		close(pushDone)
	}); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := manager.mailboxes.Executor(0).TryPost(func() {}); err != nil {
		t.Fatal(err)
	}
	close(queueFilled)
	select {
	case <-pushDone:
	case <-time.After(time.Second):
		t.Fatal("push cleanup blocked the current table command")
	}
	waitForTableTest(t, func() bool { return gameTable.seatedCount() == 0 })
	drainTable(t, manager, gameTable.ID)
	if repo.logoutCalls != 1 {
		t.Fatalf("LogoutGame calls = %d, want 1", repo.logoutCalls)
	}
}

func TestPushNotFoundKeepsGamingPlayerOfflineUntilStageCleanup(t *testing.T) {
	repo := &exitTestRepo{room: testRoomConfig()}
	manager, table := newTestTable(t, repo.room, repo)
	sess := new(captureSession)
	p := player.New(&player.Raw{
		Session:  sess,
		BaseData: &player.BaseData{UID: 42, Money: 100},
	})
	if !table.Seat(p) {
		t.Fatal("failed to arrange seated player")
	}
	p.SetGaming()
	sess.reset()
	sess.pushErr = status.Error(codes.NotFound, "binding not found")

	table.SendPacketToClient(p, v1.GameCommand_CmdActivePush, new(v1.ActivePush))
	drainTable(t, manager, table.ID)

	if !p.IsOffline() {
		t.Fatal("gaming player was not retained as offline")
	}
	if got := table.seatedCount(); got != 1 {
		t.Fatalf("seated players after gaming disconnect = %d, want 1", got)
	}
	if got := repo.logoutCalls; got != 0 {
		t.Fatalf("LogoutGame calls = %d, want 0", got)
	}
	reconnected := new(captureSession)
	p.UpdateSession(reconnected)
	table.ReEnter(p)
	if p.IsOffline() {
		t.Fatal("reconnected player remained offline")
	}
	if !reconnected.hasCommand(v1.GameCommand_CmdScenePush) {
		t.Fatal("reconnected player did not receive scene push")
	}
}

func TestPushNonNotFoundErrorDoesNotMarkPlayerOffline(t *testing.T) {
	repo := &exitTestRepo{room: testRoomConfig()}
	_, table := newTestTable(t, repo.room, repo)
	sess := new(captureSession)
	p := player.New(&player.Raw{
		Session:  sess,
		BaseData: &player.BaseData{UID: 42, Money: 100},
	})
	if !table.Seat(p) {
		t.Fatal("failed to arrange seated player")
	}
	sess.reset()
	sess.pushErr = status.Error(codes.Unavailable, "temporary failure")

	table.SendPacketToClient(p, v1.GameCommand_CmdActivePush, new(v1.ActivePush))
	table.SendPacketToClient(p, v1.GameCommand_CmdActivePush, new(v1.ActivePush))

	if p.IsOffline() {
		t.Fatal("non-NotFound push error marked player offline")
	}
	if got := sess.attempts; got != 2 {
		t.Fatalf("push attempts = %d, want 2", got)
	}
}

func TestOfflineGamingPlayerIsReclaimedAfterRoundReset(t *testing.T) {
	repo := &exitTestRepo{room: testRoomConfig()}
	_, table := newTestTable(t, repo.room, repo)
	p := player.New(&player.Raw{BaseData: &player.BaseData{UID: 42, Money: 100}})
	if !table.Seat(p) {
		t.Fatal("failed to arrange seated player")
	}
	p.SetGaming()
	p.SetOffline(true)

	table.Reset()
	table.stage.Set(StWait, time.Second, 0)
	table.checkKick()

	if got := repo.logoutCalls; got != 1 {
		t.Fatalf("LogoutGame calls = %d, want 1", got)
	}
	if got := table.seatedCount(); got != 0 {
		t.Fatalf("seated players after round cleanup = %d, want 0", got)
	}
}

type exitTestRepo struct {
	room        *conf.Room
	logoutCalls int
}

func (repo *exitTestRepo) LogoutGame(*player.Player, int32, string) error {
	repo.logoutCalls++
	return nil
}

func testRoomConfig() *conf.Room {
	return &conf.Room{
		Table:    &conf.Room_Table{TableNum: 1, ChairNum: 4},
		Game:     &conf.Room_Game{MaxMoney: -1, BaseMoney: 1},
		Robot:    &conf.Room_Robot{},
		LogCache: &conf.Room_LogCache{},
	}
}

func newPushTestTable(t *testing.T) (*Table, *player.Player, *player.Player, *captureSession, *captureSession) {
	t.Helper()
	repo := &exitTestRepo{room: testRoomConfig()}
	_, table := newTestTable(t, repo.room, repo)
	actorSession := new(captureSession)
	observerSession := new(captureSession)
	actor := player.New(&player.Raw{
		Session:  actorSession,
		BaseData: &player.BaseData{UID: 41, Money: 100},
	})
	observer := player.New(&player.Raw{
		Session:  observerSession,
		BaseData: &player.BaseData{UID: 42, Money: 100},
	})
	if !table.Seat(actor) || !table.Seat(observer) {
		t.Fatal("failed to arrange players")
	}
	return table, actor, observer, actorSession, observerSession
}

func newTestTable(t *testing.T, room *conf.Room, repo Repo) (*Manager, *Table) {
	t.Helper()
	manager := newManager(room, repo, 1, 16, 8)
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

func drainTable(t *testing.T, manager *Manager, tableID int32) {
	t.Helper()
	if err := manager.call(context.Background(), tableID, func(*Table) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func playerInfoByID(t *testing.T, scene *v1.SceneRsp, uid int64) *v1.PlayerInfo {
	t.Helper()
	for _, info := range scene.Players {
		if info.UserId == uid {
			return info
		}
	}
	t.Fatalf("player %d missing from scene", uid)
	return nil
}

type pushRecord struct {
	command int32
	message proto.Message
}

type captureSession struct {
	pushes   []pushRecord
	pushErr  error
	attempts int
}

func (*captureSession) UID() string                      { return "42" }
func (*captureSession) BindingToken() string             { return "binding-test" }
func (*captureSession) BindNode(context.Context) error   { return nil }
func (*captureSession) UnbindNode(context.Context) error { return nil }
func (sess *captureSession) Push(_ context.Context, command int32, message proto.Message) error {
	sess.attempts++
	if sess.pushErr != nil {
		return sess.pushErr
	}
	sess.pushes = append(sess.pushes, pushRecord{command: command, message: proto.Clone(message)})
	return nil
}

func (sess *captureSession) reset() {
	sess.pushes = nil
	sess.attempts = 0
}

func (sess *captureSession) lastActionPush(t *testing.T) *v1.PlayerActionRsp {
	t.Helper()
	for index := len(sess.pushes) - 1; index >= 0; index-- {
		record := sess.pushes[index]
		if record.command == int32(v1.GameCommand_CmdPlayerActionPush) {
			return record.message.(*v1.PlayerActionRsp)
		}
	}
	t.Fatal("player action push not found")
	return nil
}

func (sess *captureSession) hasCommand(command v1.GameCommand) bool {
	for _, record := range sess.pushes {
		if record.command == int32(command) {
			return true
		}
	}
	return false
}

var _ player.Session = (*captureSession)(nil)
