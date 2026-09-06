package press

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync/atomic"
	"time"

	"yola/network/websocket"
	"yola/test/internal/xgo"
	"yola/test/whot/api/v1"

	"google.golang.org/protobuf/proto"
)

type Repo interface {
	After(time.Duration, func()) bool
	Post(func()) error
	GetContext() context.Context
	GetConfig() Press
	GetURL() string
	RecordCommand(v1.GameCommand)
}

type requestFunc func(context.Context, v1.GameCommand, proto.Message) ([]byte, int32, error)

const playerStatusSit = 1

type User struct {
	repo   Repo
	id     int64
	logout atomic.Bool
	chair  atomic.Int32
	client atomic.Pointer[websocket.Client]

	requestFn requestFunc
}

func NewUser(id int64, repo Repo) (*User, error) {
	if id <= 0 {
		return nil, fmt.Errorf("user ID must be positive")
	}
	if repo == nil {
		return nil, fmt.Errorf("user repository is required")
	}
	user := &User{repo: repo, id: id}
	if err := repo.Post(user.Init); err != nil {
		return nil, fmt.Errorf("schedule user initialization: %w", err)
	}
	return user, nil
}

func (u *User) IsFree() bool {
	if u.logout.Load() {
		return true
	}
	if client := u.client.Load(); client != nil && xgo.IsHitFloat(u.repo.GetConfig().OfflineRate) {
		client.Close()
	}
	return false
}

func (u *User) Release() {
	if client := u.client.Swap(nil); client != nil {
		client.Close()
	}
}

func (u *User) Init() {
	client, err := u.connect()
	if err != nil {
		slog.Error("initialize press user", "uid", u.id, "error", err)
		u.logout.Store(true)
		return
	}

	u.chair.Store(-1)
	u.client.Store(client)
	delay := time.Duration(xgo.RandInt(0, 5000)) * time.Millisecond
	if !u.repo.After(delay, func() {
		u.Request(v1.GameCommand_CmdLogin, &v1.LoginReq{UserID: u.id, Token: "token"})
	}) {
		slog.Warn("schedule press login", "uid", u.id)
		u.logout.Store(true)
		u.Release()
	}
}

func (u *User) connect() (*websocket.Client, error) {
	pushHandlers := map[int32]websocket.PushHandler{
		int32(v1.GameCommand_CmdUserInfoPush):    u.pushHandler(v1.GameCommand_CmdUserInfoPush, u.OnEmptyPush),
		int32(v1.GameCommand_CmdReadyPush):       u.pushHandler(v1.GameCommand_CmdReadyPush, u.OnEmptyPush),
		int32(v1.GameCommand_CmdChatPush):        u.pushHandler(v1.GameCommand_CmdChatPush, u.OnEmptyPush),
		int32(v1.GameCommand_CmdHostingPush):     u.pushHandler(v1.GameCommand_CmdHostingPush, u.OnEmptyPush),
		int32(v1.GameCommand_CmdForwardPush):     u.pushHandler(v1.GameCommand_CmdForwardPush, u.OnEmptyPush),
		int32(v1.GameCommand_CmdUserOfflinePush): u.pushHandler(v1.GameCommand_CmdUserOfflinePush, u.OnEmptyPush),
		int32(v1.GameCommand_CmdPlayerQuitPush):  u.pushHandler(v1.GameCommand_CmdPlayerQuitPush, u.OnEmptyPush),
		int32(v1.GameCommand_CmdMatchResultPush): u.pushHandler(v1.GameCommand_CmdMatchResultPush, u.OnEmptyPush),
		int32(v1.GameCommand_CmdSendCardPush):    u.pushHandler(v1.GameCommand_CmdSendCardPush, u.OnEmptyPush),
		int32(v1.GameCommand_CmdScenePush):       u.pushHandler(v1.GameCommand_CmdScenePush, u.OnEmptyPush),
		int32(v1.GameCommand_CmdActivePush):      u.pushHandler(v1.GameCommand_CmdActivePush, u.OnActivePush),
		int32(v1.GameCommand_CmdPlayerActionPush): u.pushHandler(v1.GameCommand_CmdPlayerActionPush, func(body []byte) {
			if err := u.OnActionRsp(body); err != nil {
				slog.Error("handle player action push", "uid", u.id, "error", err)
			}
		}),
		int32(v1.GameCommand_CmdMarketDrawCardPush): u.pushHandler(v1.GameCommand_CmdMarketDrawCardPush, u.OnEmptyPush),
		int32(v1.GameCommand_CmdLastCardPush):       u.pushHandler(v1.GameCommand_CmdLastCardPush, u.OnEmptyPush),
		int32(v1.GameCommand_CmdResultPush):         u.pushHandler(v1.GameCommand_CmdResultPush, u.OnResultPush),
	}
	return websocket.NewClient(
		u.repo.GetContext(),
		websocket.WithEndpoint(u.repo.GetURL()),
		websocket.WithServiceName("whot"),
		websocket.WithToken(strconv.FormatInt(u.id, 10)),
		websocket.WithPushHandler(pushHandlers),
		websocket.WithConnectFunc(u.OnConnect),
		websocket.WithDisconnectFunc(u.OnDisconnect),
	)
}

func (u *User) pushHandler(command v1.GameCommand, handler websocket.PushHandler) websocket.PushHandler {
	return func(body []byte) {
		u.repo.RecordCommand(command)
		handler(body)
	}
}

func (u *User) OnEmptyPush([]byte) {}

func (u *User) OnConnect(channel *websocket.Channel) {
	slog.Debug("press user connected", "uid", u.id, "connection_id", channel.ConnID())
}

func (u *User) OnDisconnect(channel *websocket.Channel) {
	slog.Debug("press user disconnected", "uid", u.id, "connection_id", channel.ConnID())
	u.logout.Store(true)
	u.Release()
}

func (u *User) Request(command v1.GameCommand, message proto.Message) {
	if u.repo == nil {
		slog.Warn("press repository unavailable", "uid", u.id, "command", int32(command))
		return
	}
	if u.requestFn != nil {
		u.request(nil, command, message)
		return
	}
	client := u.client.Load()
	if client == nil || !client.IsAlive() {
		slog.Warn("press client unavailable", "uid", u.id, "command", int32(command))
		return
	}
	go u.request(client, command, message)
}

func (u *User) request(client *websocket.Client, command v1.GameCommand, message proto.Message) {
	ctx, cancel := context.WithTimeout(u.repo.GetContext(), 5*time.Second)
	defer cancel()
	var (
		body []byte
		code int32
		err  error
	)
	if u.requestFn != nil {
		body, code, err = u.requestFn(ctx, command, message)
	} else {
		body, code, err = client.Request(ctx, int32(command), message)
	}
	if err != nil {
		slog.Error("press request", "uid", u.id, "command", int32(command), "error", err)
		return
	}
	if code != 0 {
		slog.Error("press gateway response", "uid", u.id, "command", int32(command), "code", code)
		return
	}
	if err := u.repo.Post(func() {
		if err := u.handleResponse(command, body); err != nil {
			slog.Error("handle press response", "uid", u.id, "command", int32(command), "error", err)
			return
		}
		u.repo.RecordCommand(command)
	}); err != nil {
		slog.Warn("schedule press response", "uid", u.id, "command", int32(command), "error", err)
	}
}

func (u *User) handleResponse(command v1.GameCommand, body []byte) error {
	switch command {
	case v1.GameCommand_CmdLogin:
		return u.OnLoginRsp(body)
	case v1.GameCommand_CmdLogout:
		return u.OnLogoutRsp(body)
	case v1.GameCommand_CmdReady:
		var reply v1.ReadyRsp
		if err := proto.Unmarshal(body, &reply); err != nil {
			return fmt.Errorf("decode ready response: %w", err)
		}
		return nil
	case v1.GameCommand_CmdScene:
		return u.OnSceneRsp(body)
	case v1.GameCommand_CmdPlayerAction:
		return u.OnActionRsp(body)
	default:
		return fmt.Errorf("unsupported response command %d", command)
	}
}

func (u *User) OnLoginRsp(data []byte) error {
	var reply v1.LoginRsp
	if err := proto.Unmarshal(data, &reply); err != nil {
		u.logout.Store(true)
		u.Release()
		return fmt.Errorf("decode login response: %w", err)
	}
	if reply.Code != 0 {
		slog.Error("login rejected", "uid", u.id, "code", reply.Code, "message", reply.Msg)
		u.logout.Store(true)
		u.Release()
		return nil
	}
	u.chair.Store(reply.ChairID)
	u.Request(v1.GameCommand_CmdScene, &v1.SceneReq{UserID: u.id})
	return nil
}

func (u *User) OnSceneRsp(data []byte) error {
	var reply v1.SceneRsp
	if err := proto.Unmarshal(data, &reply); err != nil {
		return fmt.Errorf("decode scene response: %w", err)
	}
	for _, p := range reply.Players {
		if p.UserId == u.id && p.Status == playerStatusSit {
			u.Request(v1.GameCommand_CmdReady, &v1.ReadyReq{UserID: u.id, IsReady: true})
			return nil
		}
	}
	return nil
}

func (u *User) OnActivePush(data []byte) {
	var push v1.ActivePush
	if err := proto.Unmarshal(data, &push); err != nil {
		slog.Error("decode active push", "uid", u.id, "error", err)
		return
	}
	if u.chair.Load() != push.Active {
		return
	}
	if request := actionRequest(u.id, push.CanOp); request != nil {
		u.Request(v1.GameCommand_CmdPlayerAction, request)
	}
}

func actionRequest(uid int64, options []*v1.ActionOption) *v1.PlayerActionReq {
	for _, option := range options {
		if option == nil {
			continue
		}
		request := &v1.PlayerActionReq{UserId: uid, Action: option.Action}
		switch option.Action {
		case v1.ACTION_PLAY_CARD:
			if len(option.Cards) == 0 {
				continue
			}
			request.OutCard = option.Cards[0]
		case v1.ACTION_DRAW_CARD, v1.ACTION_SKIP_TURN:
		case v1.ACTION_DECLARE_SUIT:
			if len(option.Suits) == 0 {
				continue
			}
			request.DeclareSuit = option.Suits[0]
		default:
			continue
		}
		return request
	}
	return nil
}

func (u *User) OnActionRsp(data []byte) error {
	var reply v1.PlayerActionRsp
	if err := proto.Unmarshal(data, &reply); err != nil {
		return fmt.Errorf("decode player action response: %w", err)
	}
	return nil
}

func (u *User) OnResultPush(data []byte) {
	var push v1.ResultPush
	if err := proto.Unmarshal(data, &push); err != nil {
		slog.Error("decode result push", "uid", u.id, "error", err)
		return
	}
	for _, result := range push.Results {
		if result.UserID == u.id && xgo.IsHitFloat(u.repo.GetConfig().LogoutRate) {
			u.sendLogoutReq()
			return
		}
	}
}

func (u *User) sendLogoutReq() {
	delay := time.Duration(xgo.RandInt(0, 6000)) * time.Millisecond
	if !u.repo.After(delay, func() {
		u.Request(v1.GameCommand_CmdLogout, &v1.LogoutReq{UserDBID: u.id})
	}) {
		slog.Warn("schedule press logout", "uid", u.id)
	}
}

func (u *User) OnLogoutRsp(data []byte) error {
	var reply v1.LogoutRsp
	if err := proto.Unmarshal(data, &reply); err != nil {
		return fmt.Errorf("decode logout response: %w", err)
	}
	if reply.UserID != u.id {
		return nil
	}
	u.chair.Store(-1)
	u.logout.Store(true)
	u.Release()
	return nil
}
