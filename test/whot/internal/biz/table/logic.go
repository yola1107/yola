package table

import (
	"fmt"
	"log/slog"
	"math"
	"time"

	"yola/test/internal/xgo"
	"yola/test/whot/api/v1"
	"yola/test/whot/internal/biz/player"
)

// MinStartPlayerCnt 最小开局人数
const MinStartPlayerCnt = 2

func (t *Table) OnTimer() {
	state := t.stage.State()
	if debugLogEnabled() {
		slog.Debug("table stage timeout", "stage", state, "table", t.Desc())
	}
	switch state {
	case StWait:
		t.checkCanStart()
	case StReady:
		t.onGameStart()
	case StSendCard:
		t.onSendCardTimeout()
	case StPlaying:
		t.onActionTimeout()
	case StWaitEnd:
		t.gameEnd()
	case StEnd:
		t.onEndTimeout()
	default:
		slog.Error("unhandled table stage timeout", "stage", state)
	}
}

func (t *Table) updateStage(state StageID) {
	timeout := time.Duration(state.Timeout()) * time.Second
	t.updateStageWith(state, timeout)
}

func (t *Table) updateStageWith(state StageID, duration time.Duration) {
	if timerID := t.stage.TimerID(); timerID > 0 {
		t.manager.cancelTimer(timerID)
	}
	generation := t.stage.Set(state, duration, -1)
	timerID := t.manager.after(t.ID, duration, func(current *Table) {
		if current.stage.Generation() == generation {
			current.OnTimer()
		}
	})
	t.stage.SetTimerID(generation, timerID)
	if timerID < 0 {
		slog.Error("schedule table stage timer", "table_id", t.ID, "stage", state.String())
	}

	if t.mLog.enabled() {
		t.mLog.stage(t.stage.Desc(), t.active)
	}
}

// 判断是否满足开局条件，满足则进入准备阶段
func (t *Table) checkCanStart() {
	if t.stage.State() != StWait || !t.hasEnoughReadyPlayers() {
		return
	}

	slog.Info("table has enough ready players", "table", t.Desc(), "ready_players", t.seatedCount(), "capacity", t.maxPlayers)

	// 幂等保护由 updateStage 保证
	t.updateStage(StReady)
}

func (t *Table) hasEnoughReadyPlayers() bool {
	count := 0
	baseMoney := t.rc.Game.BaseMoney
	for _, p := range t.seats {
		if p != nil && p.IsReady() && p.GetAllMoney() >= baseMoney {
			count++
		}
	}
	return count >= MinStartPlayerCnt
}

// 开局流程
func (t *Table) onGameStart() {
	if t.stage.State() != StReady {
		t.updateStage(StWait)
		return
	}

	canStart, seats := t.getReadySeats()
	if !canStart {
		t.updateStage(StWait)
		return
	}

	t.intoGaming(seats)
	t.calcBanker(seats)
	t.dispatchCard(seats)
	t.updateStage(StSendCard)

	debugEnabled := debugLogEnabled()
	fileLogEnabled := t.mLog.enabled()
	if !debugEnabled && !fileLogEnabled {
		return
	}
	infos := make([]string, 0, len(seats))
	for _, p := range seats {
		infos = append(infos, fmt.Sprintf("%d:%d", p.GetPlayerID(), p.GetChairID()))
	}

	if debugEnabled {
		slog.Debug("game started", "table", t.Desc(), "players", infos)
	}
	if fileLogEnabled {
		t.mLog.begin(t.Desc(), t.rc.Game.BaseMoney, t.seats, infos)
	}
}

// 获取所有满足准备和资金条件的玩家
func (t *Table) getReadySeats() (bool, []*player.Player) {
	baseMoney := t.rc.Game.BaseMoney
	readySeats := make([]*player.Player, 0, len(t.seats))
	for _, p := range t.seats {
		if p != nil && p.IsReady() && p.GetAllMoney() >= baseMoney {
			readySeats = append(readySeats, p)
		}
	}
	return len(readySeats) >= MinStartPlayerCnt, readySeats
}

// 检查是否自动准备
func (t *Table) checkAutoReady(p *player.Player) {
	if !t.rc.Game.AutoReady {
		return
	}
	if p != nil && !p.IsReady() && p.GetAllMoney() >= t.rc.Game.BaseMoney {
		p.SetReady()
	}
}

// 自动准备逻辑
func (t *Table) checkAutoReadyAll() {
	if !t.rc.Game.AutoReady {
		return
	}
	baseMoney := t.rc.Game.BaseMoney
	for _, p := range t.seats {
		if p != nil && !p.IsReady() && p.GetAllMoney() >= baseMoney {
			p.SetReady()
		}
	}
}

// 扣钱并设置玩家游戏状态
func (t *Table) intoGaming(seats []*player.Player) {
	baseMoney := t.rc.Game.BaseMoney
	for _, p := range seats {
		if p == nil {
			continue
		}
		p.SetGaming()
		p.IntoGaming(baseMoney)
		t.sendMatchOk(p)
	}
}

// 计算庄家
func (t *Table) calcBanker(seats []*player.Player) {
	idx := xgo.RandInt(0, len(seats))
	t.active = seats[idx].GetChairID()
	t.first = t.active
}

// 发牌流程
func (t *Table) dispatchCard(seats []*player.Player) {
	t.cards.Shuffle()

	for _, p := range seats {
		p.AddCards(t.cards.DispatchCards(5))
	}

	bottom := t.cards.SetBottom()
	t.currCard = bottom[0]
	leftNum := t.cards.GetCardNum()

	t.dispatchCardPush(seats, bottom, leftNum)
}

func (t *Table) onSendCardTimeout() {
	t.updateStage(StPlaying)
	t.broadcastActivePlayerPush()
}

func (t *Table) onActionTimeout() {
	p := t.GetActivePlayer()
	if p == nil {
		slog.Error("action timeout without active player", "table", t.Desc())
		return
	}
	if !p.IsGaming() {
		slog.Error("action timeout for inactive player", "table", t.Desc(), "player", p.Desc())
		return
	}

	req, err := t.makeAutoActionReq(p)
	if err != nil {
		slog.Error("generate timeout action", "table", t.Desc(), "error", err)
		return
	}

	t.OnPlayerActionReq(p, req, true)
}

func (t *Table) makeAutoActionReq(p *player.Player) (*v1.PlayerActionReq, error) {
	ops := t.getCanOp(p)
	if len(ops) == 0 {
		return nil, fmt.Errorf("no available options: player=%v table=%v", p.Desc(), t.Desc())
	}

	op := ops[xgo.RandInt(0, len(ops))]

	req := &v1.PlayerActionReq{
		UserId: p.GetPlayerID(),
		Action: op.Action,
	}

	switch op.Action {
	case v1.ACTION_PLAY_CARD:
		if len(op.Cards) > 0 {
			req.OutCard = op.Cards[xgo.RandInt(0, len(op.Cards))]
		}
	case v1.ACTION_DRAW_CARD:
		// no extra fields
	case v1.ACTION_DECLARE_SUIT:
		if len(op.Suits) > 0 {
			req.DeclareSuit = op.Suits[xgo.RandInt(0, len(op.Suits))]
		}
	case v1.ACTION_SKIP_TURN:
		// no extra fields
	default:
		return nil, fmt.Errorf("unexpected action=%v for player=%v", op.Action, p.Desc())
	}

	return req, nil
}

func (t *Table) gameEnd() {
	// settle结算
	obj := t.settle()
	t.broadcastResult(obj)

	// 状态进入 StEnd
	t.updateStage(StEnd)

	// 检查踢人
	t.checkKick()

	// 清理数据
	t.Reset()

	if debugLogEnabled() {
		slog.Debug("game cleanup completed", "table", t.Desc())
	}
	if t.mLog.enabled() {
		t.mLog.end(fmt.Sprintf("结束清理完成。%s %s", t.Desc(), logPlayers(t.seats)))
	}
}

func (t *Table) settle() *SettleObj {
	// winner, endType := t.calcWinner()

	var (
		winner   *player.Player
		minScore int32 = math.MaxInt32
		endType        = v1.FINISH_TYPE_DECK_EMPTY
	)
	// 计算赢家
	for _, p := range t.GetGamers() {
		if score := p.GetHandScore(); winner == nil || score < minScore {
			winner = p
			minScore = score
		}
	}
	if winner != nil && minScore == 0 {
		endType = v1.FINISH_TYPE_PLAYER_HAND_EMPTY
	}

	if winner == nil {
		slog.Error("settle without winner", "table", t.Desc())
		return nil
	}

	// 算分
	conf := t.rc.Game
	settle := &SettleObj{
		Winner:    winner,
		Users:     t.GetGamers(),
		BaseScore: conf.BaseMoney,
		TaxRate:   conf.Fee,
		EndType:   endType,
	}

	if err := settle.Settle(); err != nil {
		slog.Error("settle game", "table", t.Desc(), "error", err)
		return nil
	}

	tax := settle.TaxFee
	win := settle.WinScore
	winner.AddMoney(win)

	result := settle.GetResult()
	if t.mLog.enabled() {
		t.mLog.settle(winner, win, tax, xgo.ToJSON(result))
	}
	if debugLogEnabled() {
		slog.Debug("game settled", "table", t.Desc(), "winner", winner.Desc(), "win", win, "fee", tax, "result", result)
	}
	return settle
}

func (t *Table) onEndTimeout() {
	// 状态进入 StWait
	t.updateStage(StWait)

	// 再次检查踢人
	t.checkKick()

	// 是否自动准备
	t.checkAutoReadyAll()
}
