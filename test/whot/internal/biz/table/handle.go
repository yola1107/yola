package table

import (
	"errors"
	"fmt"
	"log/slog"

	"yola/test/internal/xgo"
	"yola/test/whot/api/v1"
	"yola/test/whot/internal/biz/player"
	"yola/test/whot/pkg/codes"
)

var ErrExitGameRejected = errors.New("table rejected player exit")

func (t *Table) OnExitGame(p *player.Player, code int32, msg string) error {
	if !p.TryBeginExit() {
		return ErrExitGameRejected
	}
	if !t.RemovePlayer(p, false) {
		p.CancelExit()
		return ErrExitGameRejected
	}
	if err := t.repo.LogoutGame(p, code, msg); err != nil {
		p.CancelExit()
		return err
	}
	return nil
}

func (t *Table) OnSceneReq(p *player.Player) *v1.SceneRsp {
	if p == nil {
		return new(v1.SceneRsp)
	}
	return t.SceneInfo(p)
}

func (t *Table) OnReadyReq(p *player.Player, ready bool) bool {
	if p == nil || p.IsGaming() {
		return false
	}
	if ready {
		p.SetReady()
	} else {
		p.SetSit()
	}
	t.broadcastReadyRsp(p, ready)
	t.checkCanStart()
	return true
}

func (t *Table) OnChatReq(p *player.Player, in *v1.ChatReq) bool {
	if p == nil || in == nil {
		return false
	}
	t.broadcastChatRsp(p, in)
	return true
}

func (t *Table) OnHosting(p *player.Player, hosting bool) bool {
	if p == nil {
		return false
	}
	if hosting {
		p.IncrTimeoutCnt(true)
	} else {
		p.ClearTimeoutCnt()
	}
	t.broadcastHostingRsp(p, hosting)
	return true
}

func (t *Table) OnOffline(p *player.Player) {
	t.mLog.offline(p)
	if !p.IsGaming() {
		if err := t.OnExitGame(p, codes.KickByBroke, "OnOffline kick by broke"); err != nil {
			slog.Error("exit offline player", "uid", p.GetPlayerID(), "error", err)
		}
		return
	}
	p.SetOffline(true)
	t.broadcastUserOffline(p)
}

func (t *Table) OnPlayerActionReq(p *player.Player, in *v1.PlayerActionReq, timeout bool) bool {
	if p == nil || !p.IsGaming() || len(t.GetGamers()) <= 1 || p.GetChairID() != t.active {
		return false
	}
	if s := t.stage.State(); s != StPlaying {
		return false
	}

	if debugLogEnabled() {
		slog.Debug("handle player action", "request", t.describePlayerAction(p, in, timeout))
	}

	switch in.Action {
	case v1.ACTION_PLAY_CARD:
		if !t.canOutCard(t.currCard, p.GetCards(), in.OutCard) {
			slog.Error("reject play card",
				"request", t.describePlayerAction(p, in, timeout),
				"allowed_actions", xgo.ToJSON(t.getCanOp(p)),
			)
			return false
		}
		t.onPlayCard(p, in.OutCard, timeout)

	case v1.ACTION_DRAW_CARD:
		if !t.canDrawCard(p) {
			slog.Error("reject draw card", "request", t.describePlayerAction(p, in, timeout))
			return false
		}
		t.onDrawCard(p, timeout)

	case v1.ACTION_SKIP_TURN:
		if t.pending == nil || t.pending.Effect != v1.CARD_EFFECT_SUSPEND {
			slog.Error("reject skip turn", "request", t.describePlayerAction(p, in, timeout))
			return false
		}
		t.onSkipTurn(p, timeout)

	case v1.ACTION_DECLARE_SUIT:
		if t.pending == nil || t.pending.Effect != v1.CARD_EFFECT_WHOT || !IsWhotCard(t.currCard) {
			slog.Error("reject declare suit", "request", t.describePlayerAction(p, in, timeout))
			return false
		}
		if suit := in.DeclareSuit; suit < v1.SUIT_CIRCLE || suit > v1.SUIT_START {
			slog.Error("reject invalid suit", "request", t.describePlayerAction(p, in, timeout))
			return false
		}
		t.onDeclareSuit(p, in.DeclareSuit, timeout)

	default:
		slog.Warn("unknown player action", "request", t.describePlayerAction(p, in, timeout))
		return false
	}
	return true
}

func (t *Table) describePlayerAction(p *player.Player, in *v1.PlayerActionReq, timeout bool) string {
	return fmt.Sprintf("p=%v, curr=%v, req=%v, pending=%v, Timeout=%v",
		p.Desc(), t.currCard, xgo.ToJSON(in), descPendingEffect(t.pending), timeout)
}

func (t *Table) onPlayCard(p *player.Player, card int32, timeout bool) {
	p.RemoveCard(card)
	t.currCard = card
	t.declareSuit = -1
	t.updatePending(p, card)
	t.broadcastPlayerAction(p, v1.ACTION_PLAY_CARD, []int32{card}, 0)
	t.mLog.play(p, card, t.pending, timeout)

	if len(p.GetCards()) == 0 {
		t.updateStage(StWaitEnd)
		return
	}

	// MARKET:14牌：所有其他玩家各抽一张, 发牌不够了游戏结束
	if t.pending != nil && t.pending.Effect == v1.CARD_EFFECT_MARKET && Number(card) == marketCardNumber {
		t.pending = nil
		if deckEmpty := t.drawCardByMarket(p); deckEmpty {
			t.updateStage(StWaitEnd)
			return
		}
	}

	t.active = t.getNextActiveChair()
	if t.pending != nil {
		t.active = t.pending.Target
	}
	t.updateStage(StPlaying)
	t.broadcastActivePlayerPush()
}

func (t *Table) updatePending(p *player.Player, card int32) {
	t.pending = nil
	if !IsSpecialCard(card) {
		return
	}

	nextChair := int32(-1)
	if next := t.NextPlayer(p.GetChairID()); next != nil {
		nextChair = next.GetChairID()
	}

	pending := &v1.Pending{
		Initiator: p.GetChairID(),
		Target:    p.GetChairID(),
		Effect:    v1.CARD_EFFECT_NORMAL,
		Quantity:  1,
	}

	switch Number(card) {
	case holdOnCardNumber:
		pending.Effect = v1.CARD_EFFECT_HOLD_ON
	case pickTwoCardNumber:
		pending.Effect = v1.CARD_EFFECT_PICK_TWO
		pending.Target = nextChair
		pending.Quantity = 2
	case suspendCardNumber:
		pending.Effect = v1.CARD_EFFECT_SUSPEND
		pending.Target = nextChair
	case marketCardNumber:
		pending.Effect = v1.CARD_EFFECT_MARKET
	case whotCardNumber:
		pending.Effect = v1.CARD_EFFECT_WHOT
	}
	t.pending = pending
}

func (t *Table) drawCardByMarket(p *player.Player) bool {
	start := p.GetChairID()
	if start < 0 || start >= t.maxPlayers {
		return false
	}

	chair := start
	for {
		chair = (chair + 1) % t.maxPlayers
		if chair == start {
			break // 一圈结束，不包括自己
		}

		targetPlayer := t.seats[chair]
		if targetPlayer == nil || !targetPlayer.IsGaming() {
			continue
		}

		drawn := t.cards.DispatchCards(1)
		if len(drawn) == 0 {
			return true // 牌堆空了
		}

		targetPlayer.AddCards(drawn)
		t.sendMarketDrawCardPush(targetPlayer, drawn)
		t.mLog.market(targetPlayer, drawn, t.pending, false)
		if debugLogEnabled() {
			slog.Debug("market draw", "player", targetPlayer.Desc(), "cards", drawn)
		}
	}

	return t.cards.IsEmpty()
}

func (t *Table) onDrawCard(p *player.Player, timeout bool) {
	count := int32(1)
	if t.pending != nil && t.pending.Quantity > 0 {
		count = t.pending.Quantity
	}

	drawn := t.cards.DispatchCards(int(count))
	if len(drawn) == 0 || t.cards.IsEmpty() {
		slog.Debug("end game because card deck is exhausted", "remaining_cards", t.cards.GetCardNum())
		t.updateStage(StWaitEnd)
		return
	}

	if t.pending != nil && t.pending.Target == p.GetChairID() {
		if debugLogEnabled() {
			slog.Debug("draw card resolved pending effect", "player", p.Desc(), "pending", descPending(t.pending))
		}
		t.mLog.replyPending(p, v1.ACTION_DRAW_CARD, t.pending)
		t.pending = nil
	}

	p.AddCards(drawn)
	t.broadcastPlayerAction(p, v1.ACTION_DRAW_CARD, drawn, 0)
	t.mLog.draw(p, drawn, t.pending, timeout)

	// 通知下个玩家操作
	t.active = t.getNextActiveChair()
	t.updateStage(StPlaying)
	t.broadcastActivePlayerPush()
}

func (t *Table) onSkipTurn(p *player.Player, timeout bool) {
	t.pending = nil
	t.broadcastPlayerAction(p, v1.ACTION_SKIP_TURN, nil, 0)
	t.mLog.skipTurn(p, timeout)

	// 通知下个玩家操作
	t.active = t.getNextActiveChair()
	t.updateStage(StPlaying)
	t.broadcastActivePlayerPush()
}

func (t *Table) onDeclareSuit(p *player.Player, suit v1.SUIT, timeout bool) {
	t.currCard = NewDeclareWhot(int32(suit), t.currCard) // 修改当前牌的花色
	t.declareSuit = suit
	t.pending = nil
	t.broadcastPlayerAction(p, v1.ACTION_DECLARE_SUIT, nil, suit)
	t.mLog.declareSuit(p, suit, t.currCard, timeout)

	// 通知当前玩家操作
	t.active = p.GetChairID()
	t.updateStage(StPlaying)
	t.broadcastActivePlayerPush()
}

func (t *Table) canOutCard(curr int32, hand []int32, card int32) bool {
	pending := t.pending
	for _, c := range hand {
		if c == card && canPlayCardOn(curr, card, pending) {
			return true
		}
	}
	return false
}

func canPlayCardOn(currCard, card int32, pending *v1.Pending) bool {
	suit, number := Suit(currCard), Number(currCard)
	s, n := Suit(card), Number(card)

	// 没有 pending 情况等价于普通出牌
	if pending == nil {
		return IsWhotCard(card) || s == suit || n == number
	}

	switch pending.Effect {
	case v1.CARD_EFFECT_PICK_TWO, v1.CARD_EFFECT_SUSPEND:
		return !IsWhotCard(card) && n == number

	case v1.CARD_EFFECT_MARKET, v1.CARD_EFFECT_WHOT:
		return false

	default: // CARD_EFFECT_NORMAL, HOLD_ON 等
		return IsWhotCard(card) || s == suit || n == number
	}
}

func (t *Table) canDrawCard(p *player.Player) bool {
	if t.pending == nil {
		return true
	}
	if t.pending.Target != p.GetChairID() {
		slog.Error("reject draw by non-target player", "player", p.Desc())
		return false
	}
	switch t.pending.Effect {
	case v1.CARD_EFFECT_NORMAL, v1.CARD_EFFECT_HOLD_ON, v1.CARD_EFFECT_PICK_TWO:
		return true
	default:
		return false
	}
}
