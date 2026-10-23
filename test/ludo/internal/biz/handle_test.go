package biz

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	v1 "yola/test/ludo/api/v1"
	"yola/test/ludo/internal/biz/player"
	"yola/test/ludo/internal/biz/table"
	"yola/test/ludo/internal/pressure"
	"yola/test/ludo/pkg/codes"

	grpccodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestLoginPreservesStoredMoneyAndBinds(t *testing.T) {
	uc, repo := newTestUsecase(t, 1, 2)
	repo.base.Money = 123.5
	raw := &recordingSession{uid: "42"}

	reply := login(t, uc, raw, 42)
	managed := uc.pm.GetByID(42)
	if reply.Code != codes.Success || reply.TableID != 1 || managed == nil || managed.GetAllMoney() != 123.5 {
		t.Fatalf("login state: reply=%+v player=%v", reply, managed)
	}
	if binds, unbinds := raw.counts(); binds != 1 || unbinds != 0 {
		t.Fatalf("binding calls = (%d,%d), want (1,0)", binds, unbinds)
	}
}

func TestPressureLoginResetsStoredMoney(t *testing.T) {
	uc, repo := newTestUsecase(t, 1, 2)
	repo.base.Money = 1
	token, err := pressure.Token(pressure.MoneyRange{Min: 200, Max: 400})
	if err != nil {
		t.Fatal(err)
	}
	raw := &recordingSession{uid: "42"}

	reply, err := uc.Login(context.Background(), raw, 42, token, 0)
	managed := uc.pm.GetByID(42)
	if err != nil || reply.Code != codes.Success || managed == nil || managed.GetAllMoney() < 200 || managed.GetAllMoney() > 400 {
		t.Fatalf("pressure login: reply=%+v player=%v error=%v", reply, managed, err)
	}
}

func TestLoginCreatesMissingPlayer(t *testing.T) {
	uc, repo := newTestUsecase(t, 1, 2)
	repo.base = nil
	raw := &recordingSession{uid: "42"}

	reply := login(t, uc, raw, 42)
	managed := uc.pm.GetByID(42)
	if reply.Code != codes.Success || managed == nil || managed.GetAllMoney() < uc.rc.Game.MinMoney || managed.GetAllMoney() > uc.rc.Game.MaxMoney {
		t.Fatalf("created player state: reply=%+v player=%v", reply, managed)
	}
}

func TestLoginRollsBackBindFailure(t *testing.T) {
	uc, _ := newTestUsecase(t, 1, 2)
	raw := &recordingSession{uid: "42", bindErr: errors.New("bind failed")}

	if _, err := uc.Login(context.Background(), raw, 42, "valid", 0); err == nil {
		t.Fatal("Login() error = nil, want bind failure")
	}
	if uc.pm.GetByID(42) != nil {
		t.Fatal("bind failure left player in manager")
	}
}

func TestLoginRollsBackWhenTableIsFull(t *testing.T) {
	uc, _ := newTestUsecase(t, 1, 1)
	first := &recordingSession{uid: "41"}
	if reply := login(t, uc, first, 41); reply.Code != codes.Success {
		t.Fatalf("first login = %+v", reply)
	}
	second := &recordingSession{uid: "42"}

	reply := login(t, uc, second, 42)
	if reply.Code != codes.NotEnoughTable || uc.pm.GetByID(42) != nil {
		t.Fatalf("second login state: reply=%+v managed=%v", reply, uc.pm.GetByID(42))
	}
	if binds, unbinds := second.counts(); binds != 1 || unbinds != 1 {
		t.Fatalf("rollback calls = (%d,%d), want (1,1)", binds, unbinds)
	}
}

func TestConcurrentLoginKeepsOnePlayer(t *testing.T) {
	uc, repo := newTestUsecase(t, 1, 2)
	repo.loadBarrier = new(sync.WaitGroup)
	repo.loadBarrier.Add(2)
	sessions := []*recordingSession{{uid: "42"}, {uid: "42"}}
	replies := make([]*v1.LoginRsp, len(sessions))
	errs := make([]error, len(sessions))
	var wait sync.WaitGroup
	wait.Add(len(sessions))
	for index := range sessions {
		go func(index int) {
			defer wait.Done()
			replies[index], errs[index] = uc.Login(context.Background(), sessions[index], 42, "valid", 0)
		}(index)
	}
	wait.Wait()

	successes, rejected, binds := 0, 0, 0
	for index, reply := range replies {
		if errs[index] != nil {
			t.Fatalf("Login[%d]: %v", index, errs[index])
		}
		switch reply.Code {
		case codes.Success:
			successes++
		case codes.PlayerAlreadyInTable:
			rejected++
		default:
			t.Fatalf("Login[%d] code = %d", index, reply.Code)
		}
		count, _ := sessions[index].counts()
		binds += count
	}
	if successes != 1 || rejected != 1 || binds != 1 || uc.pm.GetByID(42) == nil {
		t.Fatalf("concurrent login: success=%d rejected=%d binds=%d", successes, rejected, binds)
	}
}

func TestLogoutPersistsUnbindsAndRemovesPlayer(t *testing.T) {
	uc, repo := newTestUsecase(t, 1, 2)
	raw := &recordingSession{uid: "42"}
	if reply := login(t, uc, raw, 42); reply.Code != codes.Success {
		t.Fatalf("Login() = %+v", reply)
	}

	reply, err := uc.Logout(context.Background(), 42)
	if err != nil || reply.Code != codes.Success {
		t.Fatalf("Logout() = (%+v, %v)", reply, err)
	}
	if repo.saved() != 1 || uc.pm.GetByID(42) != nil {
		t.Fatalf("logout state: saves=%d managed=%v", repo.saved(), uc.pm.GetByID(42))
	}
	if _, unbinds := raw.counts(); unbinds != 1 {
		t.Fatalf("UnbindNode() calls = %d, want 1", unbinds)
	}
}

func TestReconnectAndTableCommandsUseCurrentSession(t *testing.T) {
	uc, _ := newTestUsecase(t, 1, 2)
	first := &recordingSession{uid: "42"}
	if reply := login(t, uc, first, 42); reply.Code != codes.Success {
		t.Fatalf("Login() = %+v", reply)
	}
	second := &recordingSession{uid: "42"}
	if reply := login(t, uc, second, 42); reply.Code != codes.Success {
		t.Fatalf("reconnect Login() = %+v", reply)
	}
	if !second.hasPush(v1.GameCommand_CmdScenePush) {
		t.Fatal("reconnect did not push current scene")
	}

	if _, err := uc.Ready(context.Background(), 42, true); err != nil {
		t.Fatalf("Ready(): %v", err)
	}
	if _, err := uc.Scene(context.Background(), 42); err != nil {
		t.Fatalf("Scene(): %v", err)
	}
	if _, err := uc.Chat(context.Background(), 42, &v1.ChatReq{UserID: 42, Msg: "hello"}); err != nil {
		t.Fatalf("Chat(): %v", err)
	}
	if _, err := uc.Hosting(context.Background(), 42, true); err != nil {
		t.Fatalf("Hosting(): %v", err)
	}
}

func TestStaleDisconnectDoesNotOfflineReconnectedPlayer(t *testing.T) {
	uc, _ := newTestUsecase(t, 1, 2)
	first := &recordingSession{uid: "42", bindingToken: "binding-a"}
	if reply := login(t, uc, first, 42); reply.Code != codes.Success {
		t.Fatalf("first Login() = %+v", reply)
	}
	second := &recordingSession{uid: "42", bindingToken: "binding-b"}
	if reply := login(t, uc, second, 42); reply.Code != codes.Success {
		t.Fatalf("reconnect Login() = %+v", reply)
	}

	if err := uc.Disconnect(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	p := uc.pm.GetByID(42)
	if p == nil || p.GetSession() != second {
		t.Fatalf("current session = %v, want reconnect session", p)
	}
	if p.IsOffline() {
		t.Fatal("stale disconnect marked the reconnected player offline")
	}

	if err := uc.Disconnect(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if current := uc.pm.GetByID(42); current != nil && !current.IsOffline() {
		t.Fatal("current disconnect left the player online")
	}
}

func TestPushNotFoundRemovesWaitingPlayer(t *testing.T) {
	uc, _ := newTestUsecase(t, 1, 2)
	first := &recordingSession{uid: "42"}
	second := &recordingSession{uid: "43"}
	if reply := login(t, uc, first, 42); reply.Code != codes.Success {
		t.Fatalf("first Login() = %+v", reply)
	}
	if reply := login(t, uc, second, 43); reply.Code != codes.Success {
		t.Fatalf("second Login() = %+v", reply)
	}
	second.setPushError(status.Error(grpccodes.NotFound, "connection closed"))

	if _, err := uc.Chat(context.Background(), 42, &v1.ChatReq{UserID: 42, Msg: "ping"}); err != nil {
		t.Fatalf("Chat(): %v", err)
	}
	if uc.pm.GetByID(43) != nil {
		t.Fatal("NotFound push left waiting player in manager")
	}
	if _, unbinds := second.counts(); unbinds != 1 {
		t.Fatalf("UnbindNode() calls = %d, want 1", unbinds)
	}
}

func TestCleanupPersistsAndUnbindsPlayers(t *testing.T) {
	repo := &memoryPlayerRepo{base: &player.BaseData{UID: 42, Money: 500}}
	uc, cleanup, err := NewUsecase("ludo-test", repo, testRoom(1, 2))
	if err != nil {
		t.Fatal(err)
	}
	raw := &recordingSession{uid: "42"}
	if reply := login(t, uc, raw, 42); reply.Code != codes.Success {
		cleanup()
		t.Fatalf("Login() = %+v", reply)
	}
	p := uc.pm.GetByID(42)

	cleanup()
	cleanup()
	if repo.saved() != 1 || uc.pm.GetByID(42) != nil {
		t.Fatalf("cleanup state: saves=%d player=%v", repo.saved(), uc.pm.GetByID(42))
	}
	if _, unbinds := raw.counts(); unbinds != 1 {
		t.Fatalf("UnbindNode() calls = %d, want 1", unbinds)
	}
	if err := uc.tm.CallPlayer(context.Background(), p, func(*table.Table) error { return nil }); err == nil {
		t.Fatal("Table Manager still accepted work after Drain")
	}
}

func TestDrainDoesNotCleanPlayersWhenTableStopTimesOut(t *testing.T) {
	repo := &memoryPlayerRepo{base: &player.BaseData{UID: 42, Money: 500}}
	uc, cleanup, err := NewUsecase("ludo-test", repo, testRoom(1, 2))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	raw := &recordingSession{uid: "42"}
	if reply := login(t, uc, raw, 42); reply.Code != codes.Success {
		t.Fatalf("Login() = %+v", reply)
	}
	p := uc.pm.GetByID(42)
	started := make(chan struct{})
	release := make(chan struct{})
	releaseJob := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseJob)
	callDone := make(chan error, 1)
	go func() {
		callDone <- uc.tm.CallPlayer(context.Background(), p, func(*table.Table) error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err = uc.Drain(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Drain() error = %v, want %v", err, context.DeadlineExceeded)
	}
	if repo.saved() != 0 || uc.pm.GetByID(42) != p {
		t.Fatalf("timed out drain cleaned player: saves=%d player=%v", repo.saved(), uc.pm.GetByID(42))
	}
	if _, unbinds := raw.counts(); unbinds != 0 {
		t.Fatalf("UnbindNode() calls = %d, want 0", unbinds)
	}

	releaseJob()
	if err = <-callDone; err != nil {
		t.Fatalf("blocked table call error = %v", err)
	}
	if err = uc.Drain(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("repeated Drain() error = %v, want recorded deadline", err)
	}
}

func TestDrainReturnsPlayerCleanupErrors(t *testing.T) {
	saveErr := errors.New("save failed")
	unbindErr := errors.New("unbind failed")
	repo := &memoryPlayerRepo{base: &player.BaseData{UID: 42, Money: 500}, saveErr: saveErr}
	uc, cleanup, err := NewUsecase("ludo-test", repo, testRoom(1, 2))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	raw := &recordingSession{uid: "42", unbindErr: unbindErr}
	if reply := login(t, uc, raw, 42); reply.Code != codes.Success {
		t.Fatalf("Login() = %+v", reply)
	}

	err = uc.Drain(context.Background())
	if !errors.Is(err, saveErr) || !errors.Is(err, unbindErr) {
		t.Fatalf("Drain() error = %v, want save and unbind errors", err)
	}
	if repo.saved() != 1 || uc.pm.GetByID(42) == nil {
		t.Fatalf("failed drain state: saves=%d player=%v", repo.saved(), uc.pm.GetByID(42))
	}
	if _, unbinds := raw.counts(); unbinds != 1 {
		t.Fatalf("UnbindNode() calls = %d, want 1", unbinds)
	}
}
