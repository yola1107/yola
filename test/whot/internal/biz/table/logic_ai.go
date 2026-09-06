package table

import (
	"log/slog"
	"math"
	"sync/atomic"
	"time"

	"yola/test/internal/xgo"
	"yola/test/whot/api/v1"
	"yola/test/whot/internal/biz/player"

	"google.golang.org/protobuf/proto"
)

const (
	EnterMinIntervalSec = 1
	EnterMaxIntervalSec = 7
	ExitMinIntervalSec  = 3
	ExitMaxIntervalSec  = 10
	ExitRandChance      = 0.05
)

const (
	shortHandLimit         = 3
	safeOpponentHandSize   = 2
	winningWhotScore       = 100
	shortHandWhotScore     = 40
	defaultWhotScore       = -300
	urgentWinningWhotScore = 200
)

// RobotLogic 封装机器人在桌上的行为逻辑
type RobotLogic struct {
	mTable        *Table
	lastEnterUnix atomic.Int64
	lastExitUnix  atomic.Int64
}

func (r *RobotLogic) init(t *Table) {
	r.mTable = t
}

func (r *RobotLogic) markEnterNow() {
	r.lastEnterUnix.Store(time.Now().Unix())
}

func (r *RobotLogic) markExitNow() {
	r.lastExitUnix.Store(time.Now().Unix())
}

func (r *RobotLogic) EnterTooShort() bool {
	elapsedSec := time.Now().Unix() - r.lastEnterUnix.Load()
	return elapsedSec < int64(xgo.RandIntInclusive(EnterMinIntervalSec, EnterMaxIntervalSec))
}

func (r *RobotLogic) ExitTooShort() bool {
	elapsedSec := time.Now().Unix() - r.lastExitUnix.Load()
	return elapsedSec < int64(xgo.RandIntInclusive(ExitMinIntervalSec, ExitMaxIntervalSec))
}

// CanEnter 判断机器人是否能进桌
func (r *RobotLogic) CanEnter(p *player.Player) bool {
	cfg := r.mTable.rc.Robot
	if !cfg.Open {
		return false
	}

	// 控制进桌频率
	if p == nil || r.mTable == nil || r.mTable.isFull() || r.EnterTooShort() {
		return false
	}

	// 预留部分桌供机器人独立游戏。
	reservedTables := int32(0)
	if cfg.TableMaxCount > 0 && cfg.MinPlayCount > 0 {
		reservedTables = max(1, cfg.MinPlayCount/cfg.TableMaxCount)
	}

	userCount, robotCount, _, _ := r.mTable.Counter()
	switch {
	case robotCount >= cfg.TableMaxCount:
		return false
	case reservedTables > 0 && r.mTable.ID <= reservedTables:
		return true
	case userCount == 0:
		return false
	default:
		return true
	}
}

// CanExit 判断机器人是否能离桌
func (r *RobotLogic) CanExit(p *player.Player) bool {
	cfg := r.mTable.rc.Robot
	if !cfg.Open {
		return true
	}
	if p == nil || r.mTable == nil || r.ExitTooShort() {
		return false
	}
	userCount, robotCount, _, _ := r.mTable.Counter()
	money := p.GetAllMoney()
	switch {
	case userCount == 0, robotCount > cfg.TableMaxCount:
		return true
	case money >= cfg.StandMaxMoney, money <= cfg.StandMinMoney:
		return true
	default:
		return xgo.IsHitFloat(ExitRandChance)
	}
}

func (r *RobotLogic) OnMessage(p *player.Player, cmd v1.GameCommand, msg proto.Message) {
	if p == nil {
		return
	}
	switch cmd {
	case v1.GameCommand_CmdActivePush:
		r.ActivePlayer(p, msg)
	case v1.GameCommand_CmdResultPush:
		r.onExit(p, msg)
	}
}

func (r *RobotLogic) onExit(p *player.Player, _ proto.Message) {
	if !r.mTable.canExitRobot(p) {
		return
	}
	r.markExitNow() // 记录离桌时间
	dur := time.Duration(xgo.RandInt(ExitMinIntervalSec, ExitMaxIntervalSec)) * time.Second
	uid := p.GetPlayerID()
	chair := p.GetChairID()
	if timerID := r.mTable.manager.after(r.mTable.ID, dur, func(*Table) {
		current := r.mTable.playerAt(chair)
		if current == nil || current.GetPlayerID() != uid || !r.mTable.canExitRobot(current) {
			return
		}
		if err := r.mTable.OnExitGame(current, 0, "ai exit"); err != nil {
			slog.Error("exit robot", "uid", uid, "error", err)
		}
	}); timerID < 0 {
		slog.Error("schedule robot exit", "uid", uid, "table_id", r.mTable.ID)
	}
}

func (r *RobotLogic) ActivePlayer(p *player.Player, msg proto.Message) {
	rsp, ok := msg.(*v1.ActivePush)
	if !ok || rsp == nil || !p.IsGaming() || p.GetChairID() != rsp.Active || p.GetChairID() != r.mTable.active {
		return
	}

	ops := r.mTable.getCanOp(p)
	if len(ops) == 0 {
		slog.Error("robot has no available action", "player", p.Desc(), "table", r.mTable.Desc())
		return
	}

	if debugLogEnabled() {
		slog.Debug("robot action options", "player", p.Desc(), "current_card", r.mTable.currCard, "options", xgo.ToJSON(ops))
	}

	// 对手的手牌数量
	opponentHandSize := r.mTable.GetMinOpponentHandSize(p)
	req := selectBestAction(p, ops, r.mTable.currCard, opponentHandSize)
	if req == nil {
		slog.Error("robot action selection failed", "player", p.Desc(), "table", r.mTable.Desc())
		return
	}

	delay := time.Duration(xgo.RandInt(1000, int(r.mTable.stage.Remaining().Milliseconds()*3/4))) * time.Millisecond
	uid := p.GetPlayerID()
	chair := p.GetChairID()
	stageGeneration := r.mTable.stage.Generation()
	if timerID := r.mTable.manager.after(r.mTable.ID, delay, func(*Table) {
		current := r.mTable.playerAt(chair)
		if current == nil || current.GetPlayerID() != uid || !current.IsGaming() {
			return
		}
		if r.mTable.active != chair || r.mTable.stage.State() != StPlaying || r.mTable.stage.Generation() != stageGeneration {
			return
		}
		r.mTable.OnPlayerActionReq(current, req, false)
	}); timerID < 0 {
		slog.Error("schedule robot action", "uid", uid, "table_id", r.mTable.ID)
	}
}

// 策略选择
func selectBestAction(p *player.Player, ops []*v1.ActionOption, currCard int32, opponentHandSize int) *v1.PlayerActionReq {
	hand := p.GetCards()
	for _, op := range ops {
		switch op.Action {
		case v1.ACTION_DECLARE_SUIT:
			return &v1.PlayerActionReq{
				UserId:      p.GetPlayerID(),
				Action:      v1.ACTION_DECLARE_SUIT,
				DeclareSuit: getMostFrequentSuit(hand, op.Suits),
			}
		case v1.ACTION_PLAY_CARD:
			if best := chooseBestCard(op.Cards, hand, currCard, opponentHandSize); best > 0 {
				return &v1.PlayerActionReq{
					UserId:  p.GetPlayerID(),
					Action:  v1.ACTION_PLAY_CARD,
					OutCard: best,
				}
			}
		case v1.ACTION_DRAW_CARD:
			return &v1.PlayerActionReq{
				UserId: p.GetPlayerID(),
				Action: v1.ACTION_DRAW_CARD,
			}
		case v1.ACTION_SKIP_TURN:
			return &v1.PlayerActionReq{
				UserId: p.GetPlayerID(),
				Action: v1.ACTION_SKIP_TURN,
			}
		}
	}
	slog.Error("robot has no valid weighted action", "player", p.Desc(), "options", ops)
	return nil
}

// 出最多的花色
func getMostFrequentSuit(hand []int32, options []v1.SUIT) v1.SUIT {
	suitCount := make(map[v1.SUIT]int)
	for _, c := range hand {
		suitCount[v1.SUIT(Suit(c))]++
	}

	var bestSuit v1.SUIT
	highest := -1
	for _, s := range options {
		if suitCount[s] > highest {
			bestSuit = s
			highest = suitCount[s]
		}
	}
	return bestSuit
}

// 出牌选择策略
func chooseBestCard(candidates, hand []int32, currCard int32, opponentHandSize int) int32 {
	if len(candidates) == 0 {
		return 0
	}

	currSuit := v1.SUIT(Suit(currCard))
	currNum := Number(currCard)

	var bestCard int32
	bestScore := math.MinInt

	for _, c := range candidates {
		score := evaluateCardScoreV2(c, currSuit, currNum, hand, opponentHandSize)
		if score > bestScore {
			bestCard = c
			bestScore = score
		}
	}
	return bestCard
}

// 优化版评分函数
func evaluateCardScoreV2(card int32, currSuit v1.SUIT, currNum int32, hand []int32, opponentHandSize int) int {
	if IsWhotCard(card) {
		switch {
		case len(hand) == 1:
			return winningWhotScore
		case len(hand) <= shortHandLimit:
			return shortHandWhotScore
		default:
			return defaultWhotScore
		}
	}

	cardSuit := v1.SUIT(Suit(card))
	cardNumber := Number(card)

	numCount := make(map[int32]int)
	suitCount := make(map[v1.SUIT]int)
	for _, handCard := range hand {
		numCount[Number(handCard)]++
		suitCount[v1.SUIT(Suit(handCard))]++
	}

	score := 0

	if cardSuit == currSuit {
		score += 10
	}
	if cardNumber == currNum {
		score += 10
	}

	score -= (3 - numCount[cardNumber]) * 4
	score -= (2 - suitCount[cardSuit]) * 3

	switch cardNumber {
	case pickTwoCardNumber:
		if opponentHandSize > safeOpponentHandSize {
			score += 10
		} else {
			score -= 5
		}
	case suspendCardNumber:
		score += 5
	case marketCardNumber:
		score += 12
	case whotCardNumber:
		if opponentHandSize > 1 {
			score += 10
		} else {
			score -= 10
		}
	}

	// 强化对手只剩1张牌时的权重
	if opponentHandSize == 1 {
		// 王牌优先极高
		if IsWhotCard(card) {
			return urgentWinningWhotScore
		}
		// 如果能匹配当前牌（阻断对手），大幅加分
		if cardSuit == currSuit || cardNumber == currNum {
			score += 50
		}
		// 关键牌号加分幅度加大
		switch cardNumber {
		case pickTwoCardNumber, marketCardNumber, whotCardNumber:
			score += 30
		case suspendCardNumber:
			score += 20
		}
	}

	// 检查是否有连锁可能
	canChain := false
	for _, handCard := range hand {
		if handCard == card {
			continue
		}
		if Suit(handCard) == int32(cardSuit) || Number(handCard) == cardNumber {
			canChain = true
			break
		}
	}
	if canChain {
		score += 5
	}

	// 手牌少时强化牌型价值
	if len(hand) <= shortHandLimit {
		score -= (3 - numCount[cardNumber]) * 6
		score -= (2 - suitCount[cardSuit]) * 5
	}

	return score
}

func (t *Table) GetMinOpponentHandSize(p *player.Player) int {
	opponentHandSize := 54
	if p == nil {
		return opponentHandSize
	}
	for _, v := range t.seats {
		if v == nil || !v.IsGaming() || v.GetChairID() == p.GetChairID() {
			continue
		}
		if len(v.GetCards()) < opponentHandSize {
			opponentHandSize = len(v.GetCards())
		}
	}
	return opponentHandSize
}
