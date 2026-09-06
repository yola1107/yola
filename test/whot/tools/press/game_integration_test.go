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
	"yola/test/whot/api/v1"
	"yola/test/whot/internal/biz"
	"yola/test/whot/internal/biz/player"
	"yola/test/whot/internal/conf"
	"yola/test/whot/internal/data"
	"yola/test/whot/internal/service"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// TestGameDelivery 保留压测玩家的异步请求及响应 mailbox，使用真实游戏服务验证消息流。
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
	uidStart := int64(2_000_000_000) + rand.Int64N(1_000_000_000)
	room := &conf.Room{
		Table: &conf.Room_Table{TableNum: int32(tableCount), ChairNum: 4},
		Game:  &conf.Room_Game{BaseMoney: 1, MinMoney: 100, MaxMoney: 1000000, AutoReady: true},
		Robot: &conf.Room_Robot{Open: robots, Num: 2, MinPlayCount: 2, TableMaxCount: 2, IdBegin: uidStart + 10000,
			MinMoney: 10000, MaxMoney: 20000, StandMinMoney: 100, StandMaxMoney: 1000000},
	}
	client := pushbench.GameRedis(t)
	repo := data.NewPlayerRepo(data.NewData(client))
	for index := range playerCount {
		require.NoError(t, repo.SavePlayer(t.Context(), &player.BaseData{UID: uidStart + int64(index), Money: 10000}))
	}
	usecase, cleanup, err := biz.NewUsecase(repo, room)
	require.NoError(t, err)
	t.Cleanup(cleanup)
	game := service.NewService(usecase)
	probe := pushbench.StartGame(t, "whot", client, game.RegisterNode, game.Drain, playerCount, 3*time.Second)
	// 连接由测试逐个装配观测 codec，后续请求和回调仍由原 User/Runner 执行。
	runner := NewRunner(t.Context(), &LoadTest{Press: Press{URL: probe.Endpoint, Interval: 100, Num: int32(playerCount)}})
	require.NoError(t, runner.Start())
	t.Cleanup(runner.Stop)
	var actions atomic.Bool
	actions.Store(true)
	tableIDs := make([]atomic.Int32, playerCount)
	started := make([]atomic.Bool, playerCount)
	users := make([]*User, playerCount)
	var results, robotActions atomic.Uint64
	for index := range playerCount {
		user := &User{id: uidStart + int64(index), repo: runner}
		users[index] = user
		user.chair.Store(-1)
		handlers := map[int32]websocket.PushHandler{
			int32(v1.GameCommand_CmdActivePush):       user.OnActivePush,
			int32(v1.GameCommand_CmdResultPush):       user.OnResultPush,
			int32(v1.GameCommand_CmdPlayerActionPush): func(body []byte) { assert.NoError(t, user.OnActionRsp(body)) },
		}
		for command, name := range v1.GameCommand_name {
			if !strings.HasSuffix(name, "Push") {
				continue
			}
			handler := handlers[command]
			handlers[command] = func(body []byte) {
				probe.RecordPush(user.id, command, body)
				runner.RecordCommand(v1.GameCommand(command))
				if command == int32(v1.GameCommand_CmdSendCardPush) {
					started[index].Store(true)
				}
				if command == int32(v1.GameCommand_CmdResultPush) {
					results.Add(1)
				}
				if robots && command == int32(v1.GameCommand_CmdPlayerActionPush) {
					var action v1.PlayerActionRsp
					if assert.NoError(t, proto.Unmarshal(body, &action)) && action.UserId >= room.Robot.IdBegin {
						robotActions.Add(1)
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
		connection, err := websocket.NewClient(runner.ctx, websocket.WithEndpoint(probe.Endpoint), websocket.WithServiceName("whot"),
			websocket.WithToken(strconv.FormatInt(user.id, 10)), websocket.WithCodec(codec), websocket.WithPushHandler(handlers),
			websocket.WithDisconnectFunc(user.OnDisconnect))
		require.NoError(t, err)
		user.client.Store(connection)
		runner.users.Store(user.id, user)
		runner.count.Add(1)
		user.Request(v1.GameCommand_CmdLogin, &v1.LoginReq{UserID: user.id, Token: "token"})
	}
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
	verified := make(map[int32]bool, tableCount)
	for _, user := range users {
		tableID := tableIDs[int(user.id-uidStart)].Load()
		if verified[tableID] {
			continue
		}
		body, code, err := user.client.Load().Request(runner.ctx, int32(v1.GameCommand_CmdScene), &v1.SceneReq{UserID: user.id})
		require.NoError(t, err)
		require.Zero(t, code)
		var scene v1.SceneRsp
		require.NoError(t, proto.Unmarshal(body, &scene))
		assertScenePlayers(t, scene.Players, tableID, uidStart, tableIDs, robots)
		verified[tableID] = true
	}
	require.Len(t, verified, tableCount)
	probe.Stop(t)
	probe.AssertDelivered(t, playerCount)
	for _, user := range users {
		require.False(t, user.logout.Load(), "player %d disconnected", user.id)
	}
	if robots {
		require.Positive(t, robotActions.Load())
	}
	t.Logf("GAME setup: game=whot tables=%d players=%d robots=%t workers=%d duration=%s result_pushes=%d robot_action_pushes=%d verified_scenes=%d commands=%v",
		tableCount, playerCount, robots, min(tableCount, 16), duration, results.Load(), robotActions.Load(), len(verified), runner.CommandCounts())
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
	case v1.GameCommand_CmdPlayerAction:
		var reply v1.PlayerActionRsp
		if err := proto.Unmarshal(body, &reply); err != nil {
			return 0, err
		}
		if reply.Code != 0 {
			return 0, fmt.Errorf("action rejected: uid=%d code=%d", uid, reply.Code)
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
