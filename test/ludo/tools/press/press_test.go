package press

import (
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"yola/test/internal/broadcastprobe"
	v1 "yola/test/ludo/api/v1"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestRunnerStopReleasesTrackedUsers(t *testing.T) {
	runner := NewRunner(Press{})
	user := trackStartingUser(t, runner, 42)
	if !user.transition(userStarting, userActive) {
		t.Fatal("mark user active failed")
	}

	runner.Stop()

	if runner.userCount() != 0 || user.stateValue() != userClosed {
		t.Fatalf("stopped runner: users=%d state=%v", runner.userCount(), user.stateValue())
	}
}

func TestRunnerStartFailsWhenAuthenticationProbeCannotConnect(t *testing.T) {
	var config net.ListenConfig
	listener, err := config.Listen(t.Context(), "tcp", "127.0.0.1:0")
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

		require.Equal(t, userActive, user.stateValue())
		require.True(t, user.authenticated.Load())
		require.False(t, user.seated.Load())
		require.Zero(t, runner.stages[stageLogin].report().Attempts)
	})

	t.Run("play", func(t *testing.T) {
		runner := NewRunner(Press{Scenario: scenarioPlay})
		defer runner.Stop()
		user := trackStartingUser(t, runner, 42)
		runner.connectUser = func(*User) error { return nil }
		user.requestFn = func(command v1.GameCommand, _, response proto.Message) error {
			switch reply := response.(type) {
			case *v1.LoginRsp:
				require.Equal(t, v1.GameCommand_CmdLogin, command)
				reply.ChairID = 2
			case *v1.SceneRsp:
				require.Equal(t, v1.GameCommand_CmdScene, command)
				reply.Players = []*v1.PlayerInfo{{UserId: 42, Status: playerStatusSit}}
			case *v1.ReadyRsp:
				require.Equal(t, v1.GameCommand_CmdReady, command)
				reply.IsReady = true
			}
			return nil
		}

		runner.startUser(user)

		require.Equal(t, userActive, user.stateValue())
		require.True(t, user.authenticated.Load())
		require.True(t, user.seated.Load())
		require.True(t, user.ready.Load())
		for stage := stageConnect; stage < startupStageCount; stage++ {
			report := runner.stages[stage].report()
			require.Equal(t, int64(1), report.Attempts, "stage %s", startupStageNames[stage])
			require.Equal(t, int64(1), report.Succeeded, "stage %s", startupStageNames[stage])
		}
	})
}

func TestUserDrivesReadySceneDiceAndMoveCommands(t *testing.T) {
	runner := NewRunner(Press{})
	defer runner.Stop()
	var moveRequest *v1.MoveReq
	user := &User{runner: runner, id: 42, targetTableID: 7}
	user.requestFn = func(command v1.GameCommand, request, response proto.Message) error {
		switch reply := response.(type) {
		case *v1.LoginRsp:
			require.Equal(t, v1.GameCommand_CmdLogin, command)
			login := requireMessage[*v1.LoginReq](t, request)
			require.Equal(t, user.targetTableID, login.TableID)
			reply.ChairID = 2
		case *v1.SceneRsp:
			require.Equal(t, v1.GameCommand_CmdScene, command)
			reply.Players = []*v1.PlayerInfo{{UserId: 42, Color: 1, Status: playerStatusSit}}
			reply.Pieces = []*v1.Piece{{Id: 7, Pos: 13, Color: 1, Status: 1}}
		case *v1.ReadyRsp:
			require.Equal(t, v1.GameCommand_CmdReady, command)
			reply.IsReady = true
		case *v1.MoveRsp:
			require.Equal(t, v1.GameCommand_CmdMove, command)
			moveRequest = proto.CloneOf(requireMessage[*v1.MoveReq](t, request))
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

func TestSendMoveUsesLegalSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name   string
		pieces []*v1.Piece
		dices  []int32
		move   *v1.MoveReq
	}{
		{
			name: "skip arrived and idle pieces",
			pieces: []*v1.Piece{
				{Id: 1, Pos: 106, Color: 0, Status: 3},
				{Id: 2, Pos: -1, Color: 0, Status: 0},
				{Id: 3, Pos: 12, Color: 0, Status: 1},
			},
			dices: []int32{4},
			move:  &v1.MoveReq{UserId: 42, PieceId: 3, DiceValue: 4},
		},
		{
			name: "reject illegal snapshots",
			pieces: []*v1.Piece{
				{Id: 1, Pos: -1, Color: 0, Status: 0},
				{Id: 2, Pos: 106, Color: 0, Status: 3},
				{Id: 3, Pos: 105, Color: 0, Status: 2},
				{Id: 4, Pos: 0, Color: 0, Status: 3},
			},
			dices: []int32{2, 5},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := NewRunner(Press{})
			t.Cleanup(runner.Stop)
			user := &User{runner: runner, id: 42}
			user.updatePieces(0, tc.pieces)
			var moves []*v1.MoveReq
			user.requestFn = func(command v1.GameCommand, message, _ proto.Message) error {
				require.Equal(t, v1.GameCommand_CmdMove, command)
				moves = append(moves, proto.CloneOf(requireMessage[*v1.MoveReq](t, message)))
				return nil
			}

			user.sendMove(tc.dices)

			if tc.move == nil {
				require.Empty(t, moves)
				return
			}
			require.Len(t, moves, 1)
			require.True(t, proto.Equal(tc.move, moves[0]), "Move request = %v, want %v", moves[0], tc.move)
		})
	}
}

func TestSendMoveUsesResponsePieceState(t *testing.T) {
	runner := NewRunner(Press{})
	defer runner.Stop()
	user := &User{runner: runner, id: 42}
	user.updatePieces(0, []*v1.Piece{{Id: 3, Pos: 105, Color: 0, Status: 2}})
	requests := 0
	user.requestFn = func(_ v1.GameCommand, _, response proto.Message) error {
		requests++
		requireMessage[*v1.MoveRsp](t, response).Pieces = []*v1.Piece{{Id: 3, Pos: 106, Color: 0, Status: 3}}
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
	user := &User{
		runner: runner,
		id:     42,
		requestFn: func(v1.GameCommand, proto.Message, proto.Message) error {
			return errors.New("request failed")
		},
	}
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
	user.requestFn = func(command v1.GameCommand, _, response proto.Message) error {
		if command == v1.GameCommand_CmdScene {
			requireMessage[*v1.SceneRsp](t, response).Players = []*v1.PlayerInfo{{UserId: 42, Status: playerStatusReady}}
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
				URL:               "ws://127.0.0.1:3102/",
				Open:              true,
				Scenario:          scenarioPlay,
				Num:               10,
				Batch:             []int32{1, 1},
				Interval:          1,
				StartID:           600000,
				UIDCount:          100,
				GatewayLimit:      100,
				GatewayPerIPLimit: 100,
				NodeSeatCapacity:  100,
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
	go func() {
		_ = runner.runAction(func() {
			close(firstStarted)
			<-release
		})
	}()
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

func requireMessage[T proto.Message](t *testing.T, message proto.Message) T {
	t.Helper()
	typed, ok := message.(T)
	require.True(t, ok, "got protobuf %T, want %T", message, typed)
	return typed
}
