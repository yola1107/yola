package press

import (
	"context"
	"errors"
	"net"
	"testing"

	"yola/test/internal/mailbox"
	"yola/test/whot/api/v1"
	"yola/test/whot/pkg/codes"

	"google.golang.org/protobuf/proto"
)

func TestRejectedLoginMarksPressUserForRelease(t *testing.T) {
	user := &User{id: 42}
	body, err := proto.Marshal(&v1.LoginRsp{Code: codes.TokenFail, Msg: "TOKEN_FAIL"})
	if err != nil {
		t.Fatal(err)
	}

	if err := user.OnLoginRsp(body); err != nil {
		t.Fatal(err)
	}
	if !user.IsFree() {
		t.Fatal("rejected login user was retained")
	}
}

func TestRunnerStopReleasesTrackedUsers(t *testing.T) {
	runner := NewRunner(context.Background(), &LoadTest{Press: Press{Interval: 1000}})
	if err := runner.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	runner.users.Store(int64(42), &User{id: 42})
	runner.count.Store(1)

	runner.Stop()
	if got := runner.count.Load(); got != 0 {
		t.Fatalf("tracked user count = %d, want 0", got)
	}
	if _, exists := runner.users.Load(int64(42)); exists {
		t.Fatal("stopped runner retained user")
	}
	runner.Stop()
}

func TestResultPushKeepsSeatForNextRound(t *testing.T) {
	runner := &Runner{conf: &LoadTest{Press: Press{LogoutRate: 0}}}
	user := &User{repo: runner, id: 42}
	user.chair.Store(2)
	body, err := proto.Marshal(&v1.ResultPush{Results: []*v1.PlayerResult{{UserID: 42}}})
	if err != nil {
		t.Fatal(err)
	}

	user.OnResultPush(body)

	if got := user.chair.Load(); got != 2 {
		t.Fatalf("chair after result push = %d, want 2", got)
	}
}

func TestPressUserDrivesReadySceneAndPlayerAction(t *testing.T) {
	runner := &Runner{conf: &LoadTest{Press: Press{}}, ctx: context.Background()}
	repo := &immediateRepo{Runner: runner}
	user := &User{repo: repo, id: 42}
	user.requestFn = func(_ context.Context, command v1.GameCommand, _ proto.Message) ([]byte, int32, error) {
		var response proto.Message
		switch command {
		case v1.GameCommand_CmdLogin:
			response = &v1.LoginRsp{Code: codes.Success, UserID: 42, ChairID: 2}
		case v1.GameCommand_CmdScene:
			response = &v1.SceneRsp{Players: []*v1.PlayerInfo{{UserId: 42, Status: playerStatusSit}}}
		case v1.GameCommand_CmdReady:
			response = &v1.ReadyRsp{UserID: 42, IsReady: true}
		case v1.GameCommand_CmdPlayerAction:
			response = &v1.PlayerActionRsp{Code: codes.Success, UserId: 42}
		default:
			return nil, 0, errors.New("unexpected command")
		}
		body, err := proto.Marshal(response)
		return body, 0, err
	}

	user.Request(v1.GameCommand_CmdLogin, &v1.LoginReq{UserID: 42, Token: "token"})
	for _, command := range []v1.GameCommand{
		v1.GameCommand_CmdLogin,
		v1.GameCommand_CmdScene,
		v1.GameCommand_CmdReady,
	} {
		if got := runner.CommandCount(command); got != 1 {
			t.Fatalf("%s command count = %d, want 1", command, got)
		}
	}

	activeBody, err := proto.Marshal(&v1.ActivePush{
		Active: 2,
		CanOp:  []*v1.ActionOption{{Action: v1.ACTION_DRAW_CARD, DrawCount: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	user.OnActivePush(activeBody)
	if got := runner.CommandCount(v1.GameCommand_CmdPlayerAction); got != 1 {
		t.Fatalf("PlayerAction command count = %d, want 1", got)
	}
	if got := len(runner.CommandCounts()); got != int(v1.GameCommand_CmdResultPush) {
		t.Fatalf("exposed command counts = %d, want %d", got, v1.GameCommand_CmdResultPush)
	}
}

func TestFailedPressRequestDoesNotIncrementCommandCount(t *testing.T) {
	tests := []struct {
		name      string
		requestFn requestFunc
	}{
		{
			name: "request error",
			requestFn: func(context.Context, v1.GameCommand, proto.Message) ([]byte, int32, error) {
				return nil, 0, errors.New("request failed")
			},
		},
		{
			name: "gateway code",
			requestFn: func(context.Context, v1.GameCommand, proto.Message) ([]byte, int32, error) {
				return nil, 1, nil
			},
		},
		{
			name: "decode error",
			requestFn: func(context.Context, v1.GameCommand, proto.Message) ([]byte, int32, error) {
				return []byte{0xff}, 0, nil
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &Runner{conf: &LoadTest{Press: Press{}}, ctx: context.Background()}
			user := &User{repo: &immediateRepo{Runner: runner}, id: 42, requestFn: test.requestFn}

			user.Request(v1.GameCommand_CmdScene, &v1.SceneReq{UserID: 42})

			if got := runner.CommandCount(v1.GameCommand_CmdScene); got != 0 {
				t.Fatalf("failed Scene command count = %d, want 0", got)
			}
		})
	}
}

func TestRunnerStartRejectsUnavailableGatewayAndStopsRuntime(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "ws://" + listener.Addr().String() + "/"
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(context.Background(), &LoadTest{Press: Press{
		Open:     true,
		URL:      endpoint,
		StartID:  700000,
		Interval: 1000,
	}})

	if err := runner.Start(); err == nil {
		t.Fatal("Start() error = nil for unavailable Gateway")
	}
	if err := runner.Post(func() {}); !errors.Is(err, mailbox.ErrStopped) {
		t.Fatalf("Post() after failed Start = %v, want %v", err, mailbox.ErrStopped)
	}
	runner.Stop()
}

type immediateRepo struct {
	*Runner
}

func (*immediateRepo) Post(job func()) error {
	job()
	return nil
}
