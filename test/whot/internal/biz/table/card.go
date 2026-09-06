package table

import (
	"yola/test/internal/xgo"
	"yola/test/whot/api/v1"
)

const (
	SuitMask = 100
	WhotCard = 620

	holdOnCardNumber  = int32(1)
	pickTwoCardNumber = int32(2)
	suspendCardNumber = int32(8)
	marketCardNumber  = int32(14)
	whotCardNumber    = int32(20)
)

// 生成一副牌 54张
var oneDeck = []int32{
	101, 102, 103, 104, 105, 107, 108, 110, 111, 112, 113, 114, // Circle 12
	201, 202, 203, 204, 205, 207, 208, 210, 211, 212, 213, 214, // Triangle 12
	301, 302, 303, 305, 307, 310, 311, 313, 314, // Cross 9
	401, 402, 403, 405, 407, 410, 411, 413, 414, // quare 9
	501, 502, 503, 504, 505, 507, 508, // Star 7
	WhotCard, WhotCard, WhotCard, WhotCard, WhotCard, // Whot 5
}

// NewDeclareWhot 修改 Whot 牌花色
func NewDeclareWhot(suit int32, card int32) int32 {
	return suit*SuitMask + Number(card)
}

// Suit 返回花色
func Suit(card int32) int32 {
	return card / SuitMask
}

// Number 返回点数
func Number(card int32) int32 {
	return card % SuitMask
}

// ValidBottom 合法的底牌 非功能牌
func ValidBottom(card int32) bool {
	return !IsSpecialCard(card)
}

// IsSpecialCard 功能牌说明：其中1，2，8，14，20为功能牌
func IsSpecialCard(card int32) bool {
	if IsWhotCard(card) {
		return true
	}

	suit := Suit(card)
	number := Number(card)
	isValidSuit := suit >= int32(v1.SUIT_CIRCLE) && suit <= int32(v1.SUIT_START)
	isFunctionNumber := number == holdOnCardNumber || number == pickTwoCardNumber ||
		number == suspendCardNumber || number == marketCardNumber
	return isValidSuit && isFunctionNumber
}

func IsWhotCard(card int32) bool {
	return card == WhotCard
}

// GameCards 管理牌堆。
type GameCards struct {
	index  int
	cards  []int32
	bottom []int32
}

// NewGameCards 初始化牌堆
func NewGameCards() *GameCards {
	cards := make([]int32, len(oneDeck))
	copy(cards, oneDeck)
	return &GameCards{cards: cards}
}

// Shuffle 洗牌并重置索引
func (g *GameCards) Shuffle() {
	g.index = 0
	for i := 0; i < 3; i++ {
		xgo.SliceShuffle(g.cards)
	}
}

// DispatchCards 发牌，返回 n 张牌
func (g *GameCards) DispatchCards(n int) []int32 {
	end := g.index + n
	if end > len(g.cards) {
		end = len(g.cards)
	}
	cards := xgo.SliceCopy(g.cards[g.index:end])
	g.index = end
	return cards
}

// GetCards 获取剩余牌堆
func (g *GameCards) GetCards() []int32 {
	return xgo.SliceCopy(g.cards[g.index:])
}

// GetCardNum 返回剩余牌数
func (g *GameCards) GetCardNum() int32 {
	return int32(len(g.cards) - g.index)
}

// IsEmpty 是否牌堆空了
func (g *GameCards) IsEmpty() bool {
	return g.index >= len(g.cards)
}

// SetBottom 设置底牌：从未发的牌中找第一张合法底牌
func (g *GameCards) SetBottom() []int32 {
	for i := g.index; i < len(g.cards); i++ {
		if ValidBottom(g.cards[i]) {
			g.cards[g.index], g.cards[i] = g.cards[i], g.cards[g.index]
			break
		}
	}
	g.bottom = g.DispatchCards(1)
	return g.bottom
}
