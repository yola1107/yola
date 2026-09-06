package press

import (
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"yola/test/internal/broadcastprobe"
	"yola/test/ludo/api/v1"

	"google.golang.org/protobuf/proto"
)

func TestRunnerStopReleasesTrackedUsers(t *testing.T) {
	runner := NewRunner(Press{})
	user := trackStartingUser(t, runner, 42)
	if !runner.transitionUser(user, userStarting, userActive) {
		t.Fatal("mark user active failed")
	}

	runner.Stop()

	if runner.userCount() != 0 || user.stateValue() != userClosed {
		t.Fatalf("stopped runner: users=%d state=%v", runner.userCount(), user.stateValue())
	}
}

func TestRunnerStartFailsWhenAuthenticationProbeCannotConnect(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "ws://" + listener.Addr().String()
	if err = listener.Close(); err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(Press{URL: endpoint, Open: true, Num: 1, Batch: []int32{1, 1}, Interval: 1, StartID: 600000, UIDCount: 1})
	defer runner.Stop()

	if err = runner.Start(); err == nil {
		t.Fatal("Start() error = nil, want authentication probe error")
	}
}

func TestRunnerSeparatesConnectAndPlayScenarios(t *testing.T) {
	t.Run("connect", func(t *testing.T) {
		runner := NewRunner(Press{Scenario: scenarioConnect})
		defer runner.Stop()
		user := trackStartingUser(t, runner, 42)
		runner.connectUser = func(*User) error { return nil }
		user.requestFn = func(v1.GameCommand, proto.Message, proto.Message) error {
			t.Fatal("connect scenario sent a game command")
			return nil
		}

		runner.startUser(user)

		if user.stateValue() != userActive || !user.authenticated.Load() || user.seated.Load() {
			t.Fatalf("connect user: state=%v authenticated=%t seated=%t", user.stateValue(), user.authenticated.Load(), user.seated.Load())
		}
		if report := runner.stages[stageLogin].report(); report.Attempts != 0 {
			t.Fatalf("Login attempts = %d, want 0", report.Attempts)
		}
	})

	t.Run("play", func(t *testing.T) {
		runner := NewRunner(Press{Scenario: scenarioPlay})
		defer runner.Stop()
		user := trackStartingUser(t, runner, 42)
		runner.connectUser = func(*User) error { return nil }
		user.requestFn = func(command v1.GameCommand, _ proto.Message, response proto.Message) error {
			switch command {
			case v1.GameCommand_CmdLogin:
				response.(*v1.LoginRsp).ChairID = 2
			case v1.GameCommand_CmdScene:
				response.(*v1.SceneRsp).Players = []*v1.PlayerInfo{{UserId: 42, Status: playerStatusSit}}
			case v1.GameCommand_CmdReady:
				response.(*v1.ReadyRsp).IsReady = true
			}
			return nil
		}

		runner.startUser(user)

		if user.stateValue() != userActive || !user.authenticated.Load() || !user.seated.Load() || !user.ready.Load() {
			t.Fatalf("play user: state=%v authenticated=%t seated=%t ready=%t",
				user.stateValue(), user.authenticated.Load(), user.seated.Load(), user.ready.Load())
		}
		for stage := stageConnect; stage < startupStageCount; stage++ {
			report := runner.stages[stage].report()
			if report.Attempts != 1 || report.Succeeded != 1 {
				t.Fatalf("stage %s report = %+v", startupStageNames[stage], report)
			}
		}
	})
}

func TestUserDrivesReadySceneDiceAndMoveCommands(t *testing.T) {
	runner := NewRunner(Press{})
	defer runner.Stop()
	var moveRequest *v1.MoveReq
	user := &User{runner: runner, id: 42, targetTableID: 7}
	user.requestFn = func(command v1.GameCommand, request, response proto.Message) error {
		switch command {
		case v1.GameCommand_CmdLogin:
			if login := request.(*v1.LoginReq); login.TableID != user.targetTableID {
				t.Fatalf("login table = %d, want %d", login.TableID, user.targetTableID)
			}
			response.(*v1.LoginRsp).ChairID = 2
		case v1.GameCommand_CmdScene:
			scene := response.(*v1.SceneRsp)
			scene.Players = []*v1.PlayerInfo{{UserId: 42, Color: 1, Status: playerStatusSit}}
			scene.Pieces = []*v1.Piece{{Id: 7, Pos: 13, Color: 1, Status: 1}}
		case v1.GameCommand_CmdReady:
			response.(*v1.ReadyRsp).IsReady = true
		case v1.GameCommand_CmdMove:
			moveRequest = proto.Clone(request).(*v1.MoveReq)
		}
		return nil
	}

	if err := user.login(); err != nil {
		t.Fatal(err)
	}
	status, err := user.scene()
	if err != nil {
		t.Fatal(err)
	}
	if err = user.markReady(status); err != nil {
		t.Fatal(err)
	}
	user.state.Store(int32(userActive))
	pushActive(t, user, &v1.ActivePush{Active: 2, CanAction: v1.ACTION_TYPE_AcDice})
	pushActive(t, user, &v1.ActivePush{Active: 2, CanAction: v1.ACTION_TYPE_AcMove, UnusedDices: []int32{4}})

	commands := []v1.GameCommand{
		v1.GameCommand_CmdLogin,
		v1.GameCommand_CmdReady,
		v1.GameCommand_CmdScene,
		v1.GameCommand_CmdDice,
		v1.GameCommand_CmdMove,
	}
	for _, command := range commands {
		if got := runner.commandCounts()[command.String()]; got != 1 {
			t.Errorf("command %s count = %d, want 1", command, got)
		}
	}
	if moveRequest == nil || moveRequest.UserId != 42 || moveRequest.PieceId != 7 || moveRequest.DiceValue != 4 {
		t.Fatalf("move request = %+v", moveRequest)
	}
}

func TestSendMoveSelectsOneLegalMove(t *testing.T) {
	runner := NewRunner(Press{})
	defer runner.Stop()
	user := &User{runner: runner, id: 42}
	user.updatePieces(0, []*v1.Piece{
		{Id: 1, Pos: 106, Color: 0, Status: 3},
		{Id: 2, Pos: -1, Color: 0, Status: 0},
		{Id: 3, Pos: 12, Color: 0, Status: 1},
	})
	var request *v1.MoveReq
	user.requestFn = func(_ v1.GameCommand, message, _ proto.Message) error {
		request = proto.Clone(message).(*v1.MoveReq)
		return nil
	}

	user.sendMove([]int32{4})

	if request == nil || request.PieceId != 3 || request.DiceValue != 4 {
		t.Fatalf("Move request = %+v, want piece 3 and dice 4", request)
	}
}

func TestSendMoveRejectsInvalidSnapshots(t *testing.T) {
	runner := NewRunner(Press{})
	defer runner.Stop()
	user := &User{runner: runner, id: 42}
	user.updatePieces(0, []*v1.Piece{
		{Id: 1, Pos: -1, Color: 0, Status: 0},
		{Id: 2, Pos: 106, Color: 0, Status: 3},
		{Id: 3, Pos: 105, Color: 0, Status: 2},
		{Id: 4, Pos: 0, Color: 0, Status: 3},
	})
	requests := 0
	user.requestFn = func(v1.GameCommand, proto.Message, proto.Message) error {
		requests++
		return nil
	}

	user.sendMove([]int32{2, 5})

	if requests != 0 {
		t.Fatalf("Move requests = %d, want 0", requests)
	}
}

func TestSendMoveUsesResponsePieceState(t *testing.T) {
	runner := NewRunner(Press{})
	defer runner.Stop()
	user := &User{runner: runner, id: 42}
	user.updatePieces(0, []*v1.Piece{{Id: 3, Pos: 105, Color: 0, Status: 2}})
	requests := 0
	user.requestFn = func(_ v1.GameCommand, _ proto.Message, response proto.Message) error {
		requests++
		response.(*v1.MoveRsp).Pieces = []*v1.Piece{{Id: 3, Pos: 106, Color: 0, Status: 3}}
		return nil
	}
	user.sendMove([]int32{1})
	user.sendMove([]int32{1})
	if requests != 1 {
		t.Fatalf("Move requests = %d, want 1", requests)
	}
}

func TestUserSynchronizesPiecePushes(t *testing.T) {
	runner := NewRunner(Press{})
	defer runner.Stop()
	user := &User{runner: runner, id: 42}

	scene := marshal(t, &v1.SceneRsp{Players: []*v1.PlayerInfo{{UserId: 42, Color: 0}}, Pieces: []*v1.Piece{{Id: 1, Pos: -1, Color: 0, Status: 0}}})
	user.OnScenePush(scene)
	if piece, dice, ok := user.selectMove([]int32{6}); !ok || piece != 1 || dice != 6 {
		t.Fatalf("Scene move = (%d,%d,%v)", piece, dice, ok)
	}

	user.OnSendCardPush(marshal(t, &v1.SendCardPush{UserID: 42, Color: 0, Pieces: []*v1.Piece{{Id: 2, Pos: 106, Color: 0, Status: 3}}}))
	if !user.playing.Load() {
		t.Fatal("send-card push did not mark user playing")
	}
	if _, _, ok := user.selectMove([]int32{6}); ok {
		t.Fatal("arrived piece is movable")
	}

	user.OnMovePush(marshal(t, &v1.MoveRsp{Pieces: []*v1.Piece{{Id: 2, Pos: 5, Color: 0, Status: 1}}}))
	if piece, dice, ok := user.selectMove([]int32{2}); !ok || piece != 2 || dice != 2 {
		t.Fatalf("MovePush move = (%d,%d,%v)", piece, dice, ok)
	}
}

func TestRequestCountsOnlySuccessfulCommands(t *testing.T) {
	runner := NewRunner(Press{})
	defer runner.Stop()
	user := &User{runner: runner, id: 42, requestFn: func(v1.GameCommand, proto.Message, proto.Message) error {
		return errors.New("request failed")
	}}
	if err := user.Request(v1.GameCommand_CmdLogin, new(v1.LoginReq), new(v1.LoginRsp)); err == nil {
		t.Fatal("failed request succeeded")
	}
	user.requestFn = nil
	if err := user.Request(v1.GameCommand_CmdReady, new(v1.ReadyReq), new(v1.ReadyRsp)); err == nil {
		t.Fatal("request with nil client succeeded")
	}
	counts := runner.commandCounts()
	if counts[v1.GameCommand_CmdLogin.String()] != 0 || counts[v1.GameCommand_CmdReady.String()] != 0 {
		t.Fatalf("failed command counts = %v", counts)
	}
}

func TestUserSkipsReadyWhenAlreadyPrepared(t *testing.T) {
	runner := NewRunner(Press{})
	defer runner.Stop()
	user := &User{runner: runner, id: 42}
	user.requestFn = func(command v1.GameCommand, _ proto.Message, response proto.Message) error {
		if command == v1.GameCommand_CmdScene {
			response.(*v1.SceneRsp).Players = []*v1.PlayerInfo{{UserId: 42, Status: 2}}
		}
		return nil
	}
	status, err := user.scene()
	if err != nil {
		t.Fatal(err)
	}
	if err = user.markReady(status); err != nil {
		t.Fatal(err)
	}
	if got := runner.commandCounts()[v1.GameCommand_CmdReady.String()]; got != 0 {
		t.Fatalf("Ready commands = %d, want 0", got)
	}
}

func TestResultPushKeepsSeatWithoutLogout(t *testing.T) {
	runner := NewRunner(Press{LogoutRate: 0})
	defer runner.Stop()
	user := &User{runner: runner, id: 42}
	user.state.Store(int32(userActive))
	user.chair.Store(2)
	user.OnResultPush(marshal(t, &v1.ResultPush{Results: []*v1.PlayerResult{{UserID: 42}}}))
	if user.chair.Load() != 2 || user.logout.Load() {
		t.Fatalf("result changed user: chair=%d logout=%t", user.chair.Load(), user.logout.Load())
	}
}

func TestStageMetricReportsSuccessRateAndPercentiles(t *testing.T) {
	var metric stageMetric
	for value := 1; value <= 100; value++ {
		metric.record(time.Duration(value)*time.Millisecond, value <= 80)
	}
	report := metric.report()
	if report.Attempts != 100 || report.Succeeded != 80 || report.Failed != 20 || report.SuccessRate != 0.8 {
		t.Fatalf("stage report counts = %+v", report)
	}
	if report.P50 != 50*time.Millisecond || report.P95 != 95*time.Millisecond || report.P99 != 99*time.Millisecond {
		t.Fatalf("stage report percentiles = p50:%s p95:%s p99:%s", report.P50, report.P95, report.P99)
	}
}

func TestBroadcastMetricReportsDelivery(t *testing.T) {
	runner := new(Runner)
	user := newUser(42, runner)
	payload := broadcastprobe.Encode(time.Now().Add(-time.Millisecond))
	for range broadcastLatencySampleRate {
		user.onBroadcast(payload)
	}
	user.onBroadcast(nil)
	report := runner.broadcasts.report()
	if report.Received != 129 || report.Invalid != 1 || report.P50 <= 0 || report.P95 <= 0 || report.P99 <= 0 {
		t.Fatalf("broadcast report = %+v", report)
	}
}

func TestValidatePressRejectsMixedScenarioAndCapacityOverflow(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Press)
		want   string
	}{
		{name: "missing scenario", change: func(press *Press) { press.Scenario = "" }, want: "scenario"},
		{name: "missing UID range", change: func(press *Press) { press.UIDCount = 0 }, want: "uidCount"},
		{name: "missing Gateway capacity", change: func(press *Press) { press.GatewayLimit = 0 }, want: "Gateway capacity"},
		{name: "play churn", change: func(press *Press) { press.OfflineRate = 0.1 }, want: "cannot enable churn"},
		{name: "churn without rate", change: func(press *Press) { press.Scenario = scenarioReconnectChurn }, want: "requires a churn rate"},
		{name: "churn without replacement UIDs", change: func(press *Press) {
			press.Scenario = scenarioReconnectChurn
			press.OfflineRate = 0.1
			press.UIDCount = int64(press.Num)
		}, want: "replacement UIDs"},
		{name: "Gateway capacity", change: func(press *Press) { press.GatewayLimit = 9 }, want: "Gateway capacity"},
		{name: "source IP capacity", change: func(press *Press) { press.GatewayPerIPLimit = 9 }, want: "source IP capacity"},
		{name: "Node capacity", change: func(press *Press) { press.NodeSeatCapacity = 9 }, want: "Node seat capacity"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			press := Press{
				URL: "ws://127.0.0.1:3102/", Open: true, Scenario: scenarioPlay,
				Num: 10, Batch: []int32{1, 1}, Interval: 1, StartID: 600000, UIDCount: 100,
				GatewayLimit: 100, GatewayPerIPLimit: 100, NodeSeatCapacity: 100,
			}
			test.change(&press)
			_, err := validatePress(&configFile{LoadTest: &loadTest{Press: press}})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validatePress() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLoadConfigFileValidates(t *testing.T) {
	config, press, err := LoadConfig("../../configs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = config.Close() })
	if press.Scenario != scenarioPlay || press.NodeSeatCapacity != 400 || press.UIDCount != 100000 {
		t.Fatalf("tracked press config = %+v", press)
	}
}

func TestRunnerActionsRespectConcurrency(t *testing.T) {
	runner := NewRunner(Press{ActionConcurrency: 1})
	defer runner.Stop()
	firstStarted := make(chan struct{})
	secondStarted := make(chan struct{})
	release := make(chan struct{})
	go func() { _ = runner.runAction(func() { close(firstStarted); <-release }) }()
	waitSignal(t, firstStarted, "first action did not start")
	go func() { _ = runner.runAction(func() { close(secondStarted) }) }()
	select {
	case <-secondStarted:
		t.Fatal("second action exceeded concurrency")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	waitSignal(t, secondStarted, "second action did not continue")
}

func trackStartingUser(t *testing.T, runner *Runner, id int64) *User {
	t.Helper()
	user := newUser(id, runner)
	if !runner.trackUser(user) {
		t.Fatalf("track user %d failed", id)
	}
	return user
}

func pushActive(t *testing.T, user *User, active *v1.ActivePush) {
	t.Helper()
	user.OnActivePush(marshal(t, active))
}

func marshal(t *testing.T, message proto.Message) []byte {
	t.Helper()
	body, err := proto.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func waitSignal(t *testing.T, signal <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal(message)
	}
}
