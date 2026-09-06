package press

import (
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"yola/network/websocket"
	"yola/test/internal/pushbench"
	"yola/test/ludo/api/v1"
	"yola/test/ludo/internal/biz"
	"yola/test/ludo/internal/conf"
	"yola/test/ludo/internal/data"
	"yola/test/ludo/internal/service"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// TestGameDelivery 使用原压测玩家驱动完整对局；固定到达率由 BenchmarkTableCadence 单独验证。
func TestGameDelivery(t *testing.T) {
	if os.Getenv("YOLA_REDIS_INTEGRATION") == "" || os.Getenv("YOLA_ETCD_INTEGRATION") == "" {
		t.Skip("set YOLA_REDIS_INTEGRATION and YOLA_ETCD_INTEGRATION to disposable instances")
	}
	duration := 30 * time.Second
	if configured := os.Getenv("YOLA_GAME_DURATION"); configured != "" {
		var err error
		duration, err = time.ParseDuration(configured)
		require.NoError(t, err)
	}
	require.True(t, duration >= time.Second && duration <= 30*time.Minute)
	logger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(logger) })
	for _, scenario := range []struct {
		name   string
		tables int
		robots bool
	}{
		{name: "smoke", tables: 2},
		{name: "robots", tables: 1, robots: true},
		{name: "tables=100", tables: 100},
		{name: "tables=500", tables: 500},
		{name: "tables=1000", tables: 1000},
	} {
		if !t.Run(scenario.name, func(t *testing.T) { runGameDelivery(t, scenario.tables, scenario.robots, duration) }) {
			break
		}
	}
}

func runGameDelivery(t *testing.T, tableCount int, robots bool, duration time.Duration) {
	t.Helper()
	playerCount := tableCount * 4
	if robots {
		playerCount = 1
	}
	uidStart := int64(1_000_000_000) + rand.Int64N(1_000_000_000)
	room := &conf.Room{
		Table: &conf.Room_Table{TableNum: int32(tableCount), ChairNum: 4},
		Game:  &conf.Room_Game{BaseMoney: 1, MinMoney: 100, MaxMoney: -1, AutoReady: true},
		Robot: &conf.Room_Robot{Open: robots, Num: 2, MinPlayCount: 2, TableMaxCount: 2, IdBegin: uidStart + 10000,
			MinMoney: 10000, MaxMoney: 20000, StandMinMoney: 100, StandMaxMoney: 100000},
	}
	client := pushbench.GameRedis(t)
	usecase, cleanup, err := biz.NewUsecase("delivery-test", data.NewPlayerRepo(data.NewData(client)), room)
	require.NoError(t, err)
	t.Cleanup(cleanup)
	game := service.NewService(usecase)
	probe := pushbench.StartGame(t, "ludo", client, game.RegisterNode, game.Drain, playerCount)
	runner := NewRunner(Press{
		URL: probe.Endpoint, Open: true, Scenario: scenarioPlay, Num: int32(playerCount), Batch: []int32{20, 20}, Interval: 100,
		StartID: uidStart, UIDCount: int64(playerCount), Concurrency: 100, ActionConcurrency: 1000, MinMoney: 10000, MaxMoney: 20000,
	})
	t.Cleanup(runner.Stop)
	var actions atomic.Bool
	actions.Store(true)
	tableIDs := make([]atomic.Int32, playerCount)
	started := make([]atomic.Bool, playerCount)
	var results, robotMoves atomic.Uint64
	runner.connectUser = func(user *User) error {
		index := int(user.id - uidStart)
		handlers := map[int32]websocket.PushHandler{
			int32(v1.GameCommand_CmdSendCardPush): user.OnSendCardPush,
			int32(v1.GameCommand_CmdScenePush):    user.OnScenePush,
			int32(v1.GameCommand_CmdActivePush):   user.OnActivePush,
			int32(v1.GameCommand_CmdMovePush):     user.OnMovePush,
			int32(v1.GameCommand_CmdResultPush):   user.OnResultPush,
		}
		for command, name := range v1.GameCommand_name {
			if !strings.HasSuffix(name, "Push") {
				continue
			}
			handler := handlers[command]
			handlers[command] = func(body []byte) {
				probe.RecordPush(user.id, command, body)
				if command == int32(v1.GameCommand_CmdSendCardPush) {
					started[index].Store(true)
				}
				if command == int32(v1.GameCommand_CmdResultPush) {
					results.Add(1)
				}
				if robots && command == int32(v1.GameCommand_CmdMovePush) {
					var move v1.MoveRsp
					if assert.NoError(t, proto.Unmarshal(body, &move)) && move.GetMove().GetPlayerId() >= room.Robot.IdBegin {
						robotMoves.Add(1)
					}
				}
				if actions.Load() && handler != nil {
					handler(body)
				}
			}
		}
		codec := probe.Codec(func(command int32, body []byte) error {
			tableID, err := inspectGameResponse(command, body, user.id)
			if tableID > 0 {
				tableIDs[index].Store(tableID)
			}
			return err
		})
		connection, err := websocket.NewClient(runner.ctx, websocket.WithEndpoint(probe.Endpoint), websocket.WithServiceName("ludo"),
			websocket.WithToken(strconv.FormatInt(user.id, 10)), websocket.WithCodec(codec), websocket.WithPushHandler(handlers),
			websocket.WithDisconnectFunc(user.OnDisconnect))
		if err != nil {
			return err
		}
		user.chair.Store(-1)
		user.client.Store(connection)
		return nil
	}
	require.NoError(t, runner.Start())
	require.Eventually(t, func() bool {
		if probe.Err() != nil {
			return true
		}
		for index := range playerCount {
			if !started[index].Load() {
				return false
			}
		}
		return true
	}, 90*time.Second, 20*time.Millisecond, "every player must receive a real game start")
	require.NoError(t, probe.Err())
	seats := make(map[int32]int, tableCount)
	for index := range playerCount {
		seats[tableIDs[index].Load()]++
	}
	require.Len(t, seats, tableCount)
	for tableID, count := range seats {
		require.Positive(t, tableID)
		require.Equal(t, playerCount/tableCount, count)
	}
	probe.ResetMeasurements(t)
	t.Logf("GAME phase: tables=%d started=%s duration=%s", tableCount, time.Now().Format(time.RFC3339Nano), duration)
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	actions.Store(false)
	probe.ReportMeasurements(t)
	// 等待已进入玩家事件区的同步操作退出，保持接收端存活直至服务排空。
	for _, user := range runner.userSnapshot() {
		user.eventMu.Lock()
		state := user.stateValue()
		user.eventMu.Unlock()
		require.Equal(t, userActive, state)
	}
	verified := make(map[int32]bool, tableCount)
	for _, user := range runner.userSnapshot() {
		tableID := tableIDs[int(user.id-uidStart)].Load()
		if verified[tableID] {
			continue
		}
		var scene v1.SceneRsp
		require.NoError(t, user.Request(v1.GameCommand_CmdScene, &v1.SceneReq{UserID: user.id}, &scene))
		assertScenePlayers(t, scene.Players, tableID, uidStart, tableIDs, robots)
		verified[tableID] = true
	}
	require.Len(t, verified, tableCount)
	probe.Stop(t)
	probe.AssertDelivered(t, playerCount)
	require.Zero(t, runner.closedPlayers.Load())
	require.Zero(t, runner.startupRejected.Load())
	if robots {
		require.Positive(t, robotMoves.Load())
	}
	t.Logf("GAME setup: game=ludo tables=%d players=%d robots=%t workers=%d duration=%s result_pushes=%d robot_move_pushes=%d verified_scenes=%d commands=%v",
		tableCount, playerCount, robots, min(tableCount, 16), duration, results.Load(), robotMoves.Load(), len(verified), runner.commandCounts())
}

func inspectGameResponse(command int32, body []byte, uid int64) (int32, error) {
	switch v1.GameCommand(command) {
	case v1.GameCommand_CmdLogin:
		var reply v1.LoginRsp
		if err := proto.Unmarshal(body, &reply); err != nil {
			return 0, err
		}
		if reply.Code != 0 || reply.UserID != uid || reply.TableID <= 0 {
			return 0, fmt.Errorf("invalid login: uid=%d code=%d table=%d", uid, reply.Code, reply.TableID)
		}
		return reply.TableID, nil
	case v1.GameCommand_CmdDice:
		var reply v1.DiceRsp
		if err := proto.Unmarshal(body, &reply); err != nil {
			return 0, err
		}
		if reply.Code != 0 {
			return 0, fmt.Errorf("dice rejected: uid=%d code=%d", uid, reply.Code)
		}
	case v1.GameCommand_CmdMove:
		var reply v1.MoveRsp
		if err := proto.Unmarshal(body, &reply); err != nil {
			return 0, err
		}
		if reply.Code != 0 {
			return 0, fmt.Errorf("move rejected: uid=%d code=%d", uid, reply.Code)
		}
	}
	return 0, nil
}

func assertScenePlayers(t *testing.T, players []*v1.PlayerInfo, tableID int32, uidStart int64, tableIDs []atomic.Int32, robots bool) {
	t.Helper()
	wantPlayers := 4
	if robots {
		wantPlayers = 3
	}
	require.Len(t, players, wantPlayers)
	chairs := make(map[int32]bool, wantPlayers)
	uids := make(map[int64]bool, wantPlayers)
	for _, player := range players {
		require.False(t, player.Offline)
		require.GreaterOrEqual(t, player.ChairId, int32(0))
		require.Less(t, player.ChairId, int32(4))
		require.False(t, chairs[player.ChairId])
		chairs[player.ChairId] = true
		require.False(t, uids[player.UserId])
		uids[player.UserId] = true
		if player.UserId >= uidStart && player.UserId < uidStart+int64(len(tableIDs)) {
			require.Equal(t, tableID, tableIDs[int(player.UserId-uidStart)].Load())
			continue
		}
		require.True(t, robots && player.UserId >= uidStart+10000 && player.UserId < uidStart+10000+4)
	}
	if robots {
		require.Contains(t, uids, uidStart)
	}
}
