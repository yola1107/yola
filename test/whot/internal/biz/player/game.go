package player

import (
	"context"
	"fmt"
	"log/slog"
	"slices"

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

func (p *Player) Reset() {
	p.status = StFree
	p.idleCount = 0
	p.bet = 0
	p.cards = nil
}

func (p *Player) ExitReset(nextTableID int32) {
	p.Reset()
	p.chairID.Store(-1)
	p.tableID.Store(nextTableID)
	p.status = Status(-1)
}

func (p *Player) Desc() string {
	return fmt.Sprintf("(%d %d T:%d St:%d ai:%d Hand:%v)",
		p.GetPlayerID(), p.GetChairID(), p.GetTableID(), p.GetStatus(), bool2Int(p.isRobot), p.GetCards())
}

func (p *Player) SetTableID(tableID int32) {
	p.tableID.Store(tableID)
}

func (p *Player) GetTableID() int32 {
	return p.tableID.Load()
}

func (p *Player) SetChairID(chairID int32) {
	p.chairID.Store(chairID)
}

func (p *Player) GetChairID() int32 {
	return p.chairID.Load()
}

func (p *Player) IncrTimeoutCnt(timeout bool) {
	if !timeout {
		return
	}
	p.idleCount++
}

func (p *Player) ClearTimeoutCnt() {
	p.idleCount = 0
}

func (p *Player) GetTimeoutCnt() int32 {
	return p.idleCount
}

func (p *Player) GetStatus() Status {
	return p.status
}

func (p *Player) SetSit() {
	p.status = StSit
}

func (p *Player) SetReady() {
	p.status = StReady
}

func (p *Player) IsReady() bool {
	return p.status == StReady
}

func (p *Player) SetGaming() {
	p.status = StGaming
}

func (p *Player) IsGaming() bool {
	return p.status == StGaming
}

func (p *Player) SetFold() {
	p.status = StGameFold
}

func (p *Player) IsFold() bool {
	return p.status == StGameFold
}

func (p *Player) SetLost() {
	p.status = StGameLost
}

func (p *Player) IsLost() bool {
	return p.status == StGameLost
}

func (p *Player) GetCards() []int32 {
	return p.cards
}

func (p *Player) AddCards(cs []int32) {
	p.cards = append(p.cards, cs...)
	slices.Sort(p.cards)
}

func (p *Player) RemoveCard(card int32) {
	p.cards = xgo.SliceSubtract(p.cards, []int32{card})
	slices.Sort(p.cards)
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
	p.bet += bet
}

// Settle 结算
func (p *Player) Settle(totalBet float64) float64 {
	win := totalBet
	bet := p.bet
	profit := win - bet

	if slog.Default().Enabled(context.Background(), slog.LevelDebug) {
		slog.Debug("settle player", "player", p.Desc(), "win", win, "bet", bet, "profit", profit)
	}

	return profit
}

func bool2Int(v bool) int {
	if v {
		return 1
	}
	return 0
}
