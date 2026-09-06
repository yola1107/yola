package press

import (
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"

	"yola/network/websocket"
	"yola/test/internal/broadcastprobe"
	"yola/test/internal/xgo"
	"yola/test/ludo/api/v1"

	"google.golang.org/protobuf/proto"
)

type User struct {
	runner        *Runner
	id            int64
	eventMu       sync.Mutex // bridges Login/delayed logout/disconnect with the Client callback FIFO
	state         atomic.Int32
	logout        atomic.Bool
	authenticated atomic.Bool
	seated        atomic.Bool
	ready         atomic.Bool
	playing       atomic.Bool
	chair         atomic.Int32
	client        atomic.Pointer[websocket.Client]
	requestFn     requestFunc
	color         int32
	colorSet      bool
	pieces        []pieceSnapshot
}

type requestFunc func(v1.GameCommand, proto.Message, proto.Message) error

type userState int32

const (
	userStarting userState = iota
	userActive
	userClosing
	userClosed
	userStateCount
)

type pieceSnapshot struct {
	id     int32
	pos    int32
	color  int32
	status int32
}

func newUser(id int64, runner *Runner) *User {
	return &User{runner: runner, id: id}
}

func (u *User) stateValue() userState { return userState(u.state.Load()) }

func (u *User) IsFree() bool {
	if u.logout.Load() {
		return true
	}
	if u.runner.conf.Scenario != scenarioReconnectChurn {
		return false
	}
	return u.client.Load() != nil && xgo.IsHitFloat(u.runner.conf.OfflineRate)
}

func (u *User) Release() {
	client := u.client.Swap(nil)
	if client != nil {
		client.Close()
	}
}

func (u *User) Init() error {
	client, err := u.connect()
	if err != nil {
		return err
	}
	u.chair.Store(-1)
	u.client.Store(client)
	return nil
}

func (u *User) connect() (*websocket.Client, error) {
	pushHandlers := map[int32]websocket.PushHandler{
		int32(v1.GameCommand_CmdSendCardPush): u.OnSendCardPush,
		int32(v1.GameCommand_CmdScenePush):    u.OnScenePush,
		int32(v1.GameCommand_CmdActivePush):   u.OnActivePush,
		int32(v1.GameCommand_CmdMovePush):     u.OnMovePush,
		int32(v1.GameCommand_CmdResultPush):   u.OnResultPush,
		broadcastprobe.Command:                u.onBroadcast,
	}
	return websocket.NewClient(
		u.runner.ctx,
		websocket.WithEndpoint(u.gatewayURL()),
		websocket.WithServiceName("ludo"),
		websocket.WithToken(strconv.FormatInt(u.id, 10)),
		websocket.WithPushHandler(pushHandlers),
		websocket.WithDisconnectFunc(u.OnDisconnect),
	)
}

func (u *User) onBroadcast(payload []byte) {
	sentAt, valid := broadcastprobe.Decode(payload)
	u.runner.broadcasts.record(sentAt, valid)
}

func (u *User) gatewayURL() string {
	urls := gatewayURLs(u.runner.conf.URL)
	return urls[int(u.id%int64(len(urls)))]
}

func (u *User) withEvent(job func()) {
	u.eventMu.Lock()
	defer u.eventMu.Unlock()
	state := u.stateValue()
	if u.logout.Load() || state == userClosing || state == userClosed {
		return
	}
	job()
}

func (u *User) OnDisconnect(channel *websocket.Channel) {
	slog.Debug("pressure user disconnected", "uid", u.id, "connection_id", channel.ConnID())
	u.runner.RemoveUser(u)
}

func (u *User) Request(command v1.GameCommand, request, response proto.Message) error {
	if u.requestFn != nil {
		if err := u.requestFn(command, request, response); err != nil {
			return err
		}
		u.runner.recordCommand(command)
		return nil
	}
	client := u.client.Load()
	if client == nil {
		return fmt.Errorf("request command %d: client is nil", command)
	}
	if !client.IsAlive() {
		return fmt.Errorf("request command %d: client is not alive", command)
	}
	body, code, err := client.Request(u.runner.ctx, int32(command), request)
	if err != nil {
		return fmt.Errorf("request command %d: %w", command, err)
	}
	if code != 0 {
		return fmt.Errorf("request command %d returned gateway code %d", command, code)
	}
	if response != nil {
		if err = proto.Unmarshal(body, response); err != nil {
			return fmt.Errorf("decode command %d response: %w", command, err)
		}
	}
	u.runner.recordCommand(command)
	return nil
}

func (u *User) OnActivePush(data []byte) {
	rsp := new(v1.ActivePush)
	if err := proto.Unmarshal(data, rsp); err != nil {
		slog.Error("decode active push", "uid", u.id, "error", err)
		return
	}
	u.withEvent(func() {
		if u.stateValue() != userActive || u.chair.Load() != rsp.Active {
			return
		}
		if err := u.runner.runAction(func() { u.handleActive(rsp) }); err != nil {
			slog.Debug("skip pressure user action", "uid", u.id, "error", err)
		}
	})
}

func (u *User) OnSendCardPush(data []byte) {
	rsp := new(v1.SendCardPush)
	if err := proto.Unmarshal(data, rsp); err != nil {
		slog.Error("decode send card push", "uid", u.id, "error", err)
		return
	}
	u.withEvent(func() {
		if rsp.UserID == u.id {
			u.updatePieces(rsp.Color, rsp.Pieces)
			u.playing.Store(true)
		}
	})
}

func (u *User) OnScenePush(data []byte) {
	rsp := new(v1.SceneRsp)
	if err := proto.Unmarshal(data, rsp); err != nil {
		slog.Error("decode scene push", "uid", u.id, "error", err)
		return
	}
	u.withEvent(func() { u.updateScene(rsp) })
}

func (u *User) OnMovePush(data []byte) {
	rsp := new(v1.MoveRsp)
	if err := proto.Unmarshal(data, rsp); err != nil {
		slog.Error("decode move push", "uid", u.id, "error", err)
		return
	}
	u.withEvent(func() {
		if rsp.Code == 0 {
			u.updateMoveResponse(rsp)
		}
	})
}

func (u *User) OnResultPush(data []byte) {
	rsp := new(v1.ResultPush)
	if err := proto.Unmarshal(data, rsp); err != nil {
		slog.Error("decode result push", "uid", u.id, "error", err)
		return
	}
	u.withEvent(func() {
		if u.stateValue() != userActive {
			return
		}
		u.playing.Store(false)
		if u.runner.conf.Scenario != scenarioReconnectChurn {
			return
		}
		for _, result := range rsp.Results {
			if result.UserID == u.id && xgo.IsHitFloat(u.runner.conf.LogoutRate) {
				u.sendLogoutReq()
				return
			}
		}
	})
}
