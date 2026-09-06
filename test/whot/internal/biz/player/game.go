package player

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync/atomic"

	"yola/test/internal/xgo"
)

const (
	TableIDPending  int32 = 0
	TableIDDetached int32 = -1
)

const (
	StFree Status = iota
	StSit
	StReady
	StGaming
	StGameFold
	StGameLost
)

type Status int32

func (s Status) String() string {
	switch s {
	case StFree:
		return "Free"
	case StSit:
		return "Sit"
	case StReady:
		return "Ready"
	case StGaming:
		return "Gaming"
	case StGameFold:
		return "Fold"
	case StGameLost:
		return "Lost"
	default:
		return fmt.Sprintf("%d", s)
	}
}

type GameData struct {
	tableID   atomic.Int32
	chairID   atomic.Int32
	status    Status
	idleCount int32
	isOffline atomic.Bool
	bet       float64
	cards     []int32
}

func (p *Player) Reset() {
	p.gameData.status = StFree
	p.gameData.idleCount = 0
	p.gameData.bet = 0
	p.gameData.cards = nil
}

func (p *Player) ExitReset(nextTableID int32) {
	p.Reset()
	p.gameData.chairID.Store(-1)
	p.gameData.tableID.Store(nextTableID)
	p.gameData.status = Status(-1)
}

func (p *Player) Desc() string {
	return fmt.Sprintf("(%d %d T:%d St:%d ai:%d Hand:%v)",
		p.GetPlayerID(), p.GetChairID(), p.GetTableID(), p.GetStatus(), bool2Int(p.isRobot), p.GetCards())
}

func bool2Int(v bool) int {
	if v {
		return 1
	}
	return 0
}

func (p *Player) SetTableID(tableID int32) {
	p.gameData.tableID.Store(tableID)
}

func (p *Player) GetTableID() int32 {
	return p.gameData.tableID.Load()
}

func (p *Player) SetChairID(chairID int32) {
	p.gameData.chairID.Store(chairID)
}

func (p *Player) GetChairID() int32 {
	return p.gameData.chairID.Load()
}

func (p *Player) IncrTimeoutCnt(timeout bool) {
	if !timeout {
		return
	}
	p.gameData.idleCount++
}

func (p *Player) ClearTimeoutCnt() {
	p.gameData.idleCount = 0
}

func (p *Player) GetTimeoutCnt() int32 {
	return p.gameData.idleCount
}

func (p *Player) SetOffline(offline bool) {
	p.gameData.isOffline.Store(offline)
}

func (p *Player) IsOffline() bool {
	return p.gameData.isOffline.Load()
}

func (p *Player) GetStatus() Status {
	return p.gameData.status
}

func (p *Player) SetSit() {
	p.gameData.status = StSit
}

func (p *Player) SetReady() {
	p.gameData.status = StReady
}

func (p *Player) IsReady() bool {
	return p.gameData.status == StReady
}

func (p *Player) SetGaming() {
	p.gameData.status = StGaming
}

func (p *Player) IsGaming() bool {
	return p.gameData.status == StGaming
}

func (p *Player) SetFold() {
	p.gameData.status = StGameFold
}

func (p *Player) IsFold() bool {
	return p.gameData.status == StGameFold
}

func (p *Player) SetLost() {
	p.gameData.status = StGameLost
}

func (p *Player) IsLost() bool {
	return p.gameData.status == StGameLost
}

func (p *Player) GetCards() []int32 {
	return p.gameData.cards
}

func (p *Player) AddCards(cs []int32) {
	p.gameData.cards = append(p.gameData.cards, cs...)
	p.refreshCards()
}

func (p *Player) RemoveCard(card int32) {
	p.gameData.cards = xgo.SliceSubtract(p.gameData.cards, []int32{card})
	p.refreshCards()
}

func (p *Player) refreshCards() {
	if len(p.gameData.cards) == 0 {
		return
	}
	sort.Slice(p.gameData.cards, func(i, j int) bool {
		return p.gameData.cards[i] < p.gameData.cards[j]
	})
}

func (p *Player) GetHandScore() int32 {
	var total int32
	for _, card := range p.GetCards() {
		total += card % 100
	}
	return total
}

func (p *Player) IntoGaming(bet float64) {
	p.UseMoney(bet)
	p.gameData.bet += bet
}

// Settle 结算
func (p *Player) Settle(totalBet float64) float64 {
	win := totalBet
	bet := p.gameData.bet
	profit := win - bet

	if slog.Default().Enabled(context.Background(), slog.LevelDebug) {
		slog.Debug("settle player", "player", p.Desc(), "win", win, "bet", bet, "profit", profit)
	}

	return profit
}
