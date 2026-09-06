package table

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"

	"yola/test/internal/mailbox"
	"yola/test/whot/api/v1"
	"yola/test/whot/internal/biz/player"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func (t *Table) SendPacketToClient(p *player.Player, cmd v1.GameCommand, msg proto.Message) {
	if p == nil || p.IsOffline() {
		return
	}
	if p.IsRobot() {
		t.aiLogic.OnMessage(p, cmd, msg)
		return
	}
	var err error
	if pushed, pushErr := t.manager.pushToUID(p.GetPlayerID(), int32(cmd), msg); pushed {
		err = pushErr
	} else {
		sess := p.GetSession()
		if sess == nil {
			return
		}
		err = sess.Push(context.Background(), int32(cmd), msg)
	}
	if err == nil {
		return
	}
	if status.Code(err) != codes.NotFound {
		slog.Warn("push message", "uid", p.GetPlayerID(), "command", int32(cmd), "error", err)
		return
	}

	p.SetOffline(true)
	// OnOffline can broadcast or evict the player, so run it after the current actor job completes.
	err = t.manager.tryPost(t.ID, func(*Table) { t.cleanupDisconnectedPlayer(p) })
	if errors.Is(err, mailbox.ErrFull) {
		if timerID := t.manager.after(t.ID, 0, func(*Table) { t.cleanupDisconnectedPlayer(p) }); timerID > 0 {
			return
		}
	}
	if err != nil {
		slog.Error("schedule disconnected player cleanup", "uid", p.GetPlayerID(), "error", err)
	}
}

func (t *Table) cleanupDisconnectedPlayer(p *player.Player) {
	if p == nil || !p.IsOffline() {
		return
	}
	if chair := p.GetChairID(); chair < 0 || t.playerAt(chair) != p {
		return
	}
	t.OnOffline(p)
}

func (t *Table) SendPacketToAll(cmd v1.GameCommand, msg proto.Message) {
	for _, v := range t.seats {
		if v == nil {
			continue
		}
		t.SendPacketToClient(v, cmd, msg)
	}
}

func (t *Table) SendPacketToAllExcept(cmd v1.GameCommand, msg proto.Message, uids ...int64) {
	exceptMap := make(map[int64]struct{})
	for _, v := range uids {
		exceptMap[v] = struct{}{}
	}
	for _, v := range t.seats {
		if v == nil {
			continue
		}
		if _, ok := exceptMap[v.GetPlayerID()]; ok {
			continue
		}
		t.SendPacketToClient(v, cmd, msg)
	}
}

// 广播入座信息
func (t *Table) broadcastUserInfo(p *player.Player) {
	t.sendUserInfoToAnother(p, p)
	for k, v := range t.seats {
		if v != nil && k != int(p.GetChairID()) {
			t.sendUserInfoToAnother(p, v)
			t.sendUserInfoToAnother(v, p)
		}
	}
}

func (t *Table) sendUserInfoToAnother(src *player.Player, dst *player.Player) {
	t.SendPacketToClient(dst, v1.GameCommand_CmdUserInfoPush, &v1.UserInfoPush{
		UserID:    src.GetPlayerID(),
		ChairID:   src.GetChairID(),
		UserName:  src.GetNickName(),
		Money:     src.GetAllMoney(),
		Avatar:    src.GetAvatar(),
		AvatarUrl: src.GetAvatarURL(),
		Vip:       src.GetVipGrade(),
		Status:    int32(src.GetStatus()),
	})
}

// BroadcastForwardRsp 消息转发
func (t *Table) BroadcastForwardRsp(requester *player.Player, ty int32, msg string) {
	t.SendPacketToAllExcept(v1.GameCommand_CmdForwardPush, &v1.ForwardRsp{
		Type: ty,
		Msg:  msg,
	}, requester.GetPlayerID())
}

func (t *Table) broadcastReadyRsp(p *player.Player, ready bool) {
	t.SendPacketToAllExcept(v1.GameCommand_CmdReadyPush, &v1.ReadyRsp{
		UserID:  p.GetPlayerID(),
		IsReady: ready,
	}, p.GetPlayerID())
}

func (t *Table) broadcastChatRsp(p *player.Player, in *v1.ChatReq) {
	t.SendPacketToAllExcept(v1.GameCommand_CmdChatPush, &v1.ChatRsp{
		UserID: int32(p.GetPlayerID()),
		OpType: in.OpType,
		FaceID: in.FaceID,
		Msg:    in.Msg,
	}, p.GetPlayerID())
}

func (t *Table) broadcastHostingRsp(p *player.Player, hosting bool) {
	status := int32(1)
	if hosting {
		status = 2
	}
	t.SendPacketToAllExcept(v1.GameCommand_CmdHostingPush, &v1.HostingRsp{
		ChairID: p.GetChairID(),
		Status:  status,
		AiNum:   p.GetTimeoutCnt(),
	}, p.GetPlayerID())
}

// 广播玩家断线信息
func (t *Table) broadcastUserOffline(p *player.Player) {
	t.SendPacketToAll(v1.GameCommand_CmdUserOfflinePush, &v1.UserOfflinePush{
		UserID:    p.GetPlayerID(),
		IsOffline: p.IsOffline(),
	})
}

// 玩家离桌推送
func (t *Table) broadcastUserQuitPush(p *player.Player) {
	t.SendPacketToAllExcept(v1.GameCommand_CmdPlayerQuitPush, &v1.PlayerQuitPush{
		UserID:  p.GetPlayerID(),
		ChairID: p.GetChairID(),
	}, p.GetPlayerID())
}

func (t *Table) sendMatchOk(p *player.Player) {
	t.SendPacketToClient(p, v1.GameCommand_CmdMatchResultPush, &v1.MatchResultPush{
		Code: 0,
		Msg:  "",
		Uid:  p.GetPlayerID(),
	})
}

// 发牌推送
func (t *Table) dispatchCardPush(canGameSeats []*player.Player, bottom []int32, leftNum int32) {
	for _, p := range canGameSeats {
		if p == nil {
			continue
		}
		if !p.IsGaming() {
			continue
		}
		t.SendPacketToClient(p, v1.GameCommand_CmdSendCardPush, &v1.SendCardPush{
			UserID:  p.GetPlayerID(),
			Cards:   p.GetCards(),
			Bottom:  bottom,
			LeftNum: leftNum,
		})
	}
}

// SendSceneInfo 发送游戏场景信息
func (t *Table) SceneInfo(viewer *player.Player) *v1.SceneRsp {
	roomConfig := t.rc
	return &v1.SceneRsp{
		BaseScore:   roomConfig.Game.BaseMoney,
		Stage:       int32(t.stage.State()),
		Timeout:     int64(t.stage.Remaining().Seconds()),
		Active:      t.active,
		FirstChair:  t.first,
		CurrCard:    t.currCard,
		DeclareSuit: t.declareSuit,
		LeftNum:     t.cards.GetCardNum(),
		Pending:     t.pending,
		Players:     t.getPlayersScene(viewer),
	}
}

func (t *Table) SendSceneInfo(p *player.Player) {
	t.SendPacketToClient(p, v1.GameCommand_CmdScenePush, t.SceneInfo(p))
}

func (t *Table) getPlayersScene(viewer *player.Player) []*v1.PlayerInfo {
	var players []*v1.PlayerInfo
	for _, subject := range t.seats {
		if subject == nil {
			continue
		}
		includePrivate := subject == viewer
		players = append(players, t.getScene(subject, includePrivate))
	}
	return players
}

func (t *Table) getScene(subject *player.Player, includePrivate bool) *v1.PlayerInfo {
	if subject == nil {
		return nil
	}
	info := &v1.PlayerInfo{
		UserId:  subject.GetPlayerID(),
		ChairId: subject.GetChairID(),
		Status:  int32(subject.GetStatus()),
		Hosting: subject.GetTimeoutCnt() > 0,
		Offline: subject.IsOffline(),
	}
	if includePrivate {
		info.Cards = subject.GetCards()
		info.CanOp = t.getCanOp(subject)
	}
	return info
}

// 当前活动玩家推送
func (t *Table) broadcastActivePlayerPush() {
	for _, p := range t.seats {
		if p == nil {
			continue
		}
		rsp := &v1.ActivePush{
			Stage:    int32(t.stage.State()),
			Timeout:  int64(t.stage.Remaining().Seconds()),
			Active:   t.active,
			LeftNum:  t.cards.GetCardNum(),
			CardsNum: int32(len(p.GetCards())),
			Pending:  t.pending,
			CanOp:    t.getCanOp(p),
		}
		if p.GetChairID() == t.active && p.IsGaming() {
			t.mLog.activePush(p, t.currCard, t.pending, rsp.CanOp)
		}
		t.SendPacketToClient(p, v1.GameCommand_CmdActivePush, rsp)
	}
}

func (t *Table) broadcastPlayerAction(p *player.Player, action v1.ACTION, cs []int32, declaredSuit v1.SUIT) {
	for _, v := range t.seats {
		if v == nil {
			continue
		}

		hand := p.GetCards()
		self := v.GetPlayerID() == p.GetPlayerID()

		rsp := &v1.PlayerActionRsp{
			Code:    0,
			Message: "",
			UserId:  p.GetPlayerID(),
			ChairId: p.GetChairID(),
			Action:  action,
			LeftNum: t.cards.GetCardNum(),
			Effect:  t.pending,
			PlayResult: &v1.PlayCardResult{
				CardsNum: int32(len(hand)),
				Card:     0,
				Cards:    nil,
			},
			DrawResult: &v1.DrawCardResult{
				CardsNum: int32(len(hand)),
				DrawNum:  0,
				Cards:    nil,
			},
			DeclareResult: &v1.DeclareSuitResult{
				Suit: declaredSuit,
			},
		}

		switch action {
		case v1.ACTION_PLAY_CARD:
			if len(cs) == 1 {
				rsp.PlayResult.Card = cs[0]
			}
			if self {
				rsp.PlayResult.Cards = hand
			}
		case v1.ACTION_DRAW_CARD:
			rsp.DrawResult.DrawNum = int32(len(cs))
			if self {
				rsp.DrawResult.Drawn = cs
				rsp.DrawResult.Cards = hand
			}
		}

		t.SendPacketToClient(v, v1.GameCommand_CmdPlayerActionPush, rsp)
	}

	if debugLogEnabled() {
		declaredSuitValue := ""
		if declaredSuit > 0 {
			declaredSuitValue = fmt.Sprintf("%d", declaredSuit)
		}
		slog.Debug("broadcast player action",
			"player", p.Desc(),
			"action", action.String(),
			"cards", cs,
			"declared_suit", declaredSuitValue,
			"pending", descPendingEffect(t.pending),
			"current_card", t.currCard,
		)
	}
}

func (t *Table) getCanOp(p *player.Player) []*v1.ActionOption {
	if p == nil || !p.IsGaming() || len(t.GetGamers()) <= 1 || p.GetChairID() != t.active {
		return nil
	}
	switch t.stage.State() {
	case StWait, StReady, StSendCard, StWaitEnd, StEnd:
		return nil
	default:
	}

	var ops []*v1.ActionOption
	pending := t.pending

	switch {
	case pending == nil || pending.Effect == v1.CARD_EFFECT_NORMAL, pending.Effect == v1.CARD_EFFECT_HOLD_ON:
		ops = append(ops, newDrawOption(1))

	case pending.Effect == v1.CARD_EFFECT_PICK_TWO: // 出牌出点数一样的
		ops = append(ops, newDrawOption(pending.Quantity))

	case pending.Effect == v1.CARD_EFFECT_SUSPEND: // 出牌出点数一样的
		ops = append(ops, &v1.ActionOption{Action: v1.ACTION_SKIP_TURN})

	case pending.Effect == v1.CARD_EFFECT_WHOT:
		suits := []v1.SUIT{v1.SUIT_CIRCLE, v1.SUIT_TRIANGLE, v1.SUIT_CROSS, v1.SUIT_SQUARE, v1.SUIT_START}
		return []*v1.ActionOption{
			{Action: v1.ACTION_DECLARE_SUIT, Suits: suits},
		}
	}

	canOuts := collectPlayCard(t.currCard, p.GetCards(), pending)
	if len(canOuts) > 0 {
		ops = append(ops, &v1.ActionOption{Action: v1.ACTION_PLAY_CARD, Cards: canOuts})
	}

	if len(ops) > 1 {
		sort.Slice(ops, func(i, j int) bool {
			return ops[i].Action < ops[j].Action
		})
	}

	return ops
}

func newDrawOption(n int32) *v1.ActionOption {
	return &v1.ActionOption{Action: v1.ACTION_DRAW_CARD, DrawCount: n}
}

func collectPlayCard(currCard int32, hand []int32, pending *v1.Pending) []int32 {
	out := make([]int32, 0, len(hand))
	for _, card := range hand {
		if canPlayCardOn(currCard, card, pending) {
			out = append(out, card)
		}
	}
	return out
}

func (t *Table) sendMarketDrawCardPush(p *player.Player, draw []int32) {
	for _, v := range t.seats {
		if v == nil {
			continue
		}
		rsp := &v1.MarketDrawCardPush{
			UserID:   p.GetPlayerID(),
			ChairID:  p.GetChairID(),
			DrawNum:  int32(len(draw)),
			CardsNum: int32(len(p.GetCards())),
			LeftNum:  t.cards.GetCardNum(),
			Draw:     nil,
			Cards:    nil,
		}
		if v.GetPlayerID() == p.GetPlayerID() {
			rsp.Draw = draw
			rsp.Cards = p.GetCards()
		}
		t.SendPacketToClient(v, v1.GameCommand_CmdMarketDrawCardPush, rsp)
	}
}

func (t *Table) broadcastResult(obj *SettleObj) {
	rsp := &v1.ResultPush{}
	if obj != nil {
		rsp = obj.GetResult()
	}
	t.SendPacketToAll(v1.GameCommand_CmdResultPush, rsp)
}
