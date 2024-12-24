package calc

/*
	GameCards 游戏用牌
*/

//一副牌
var _deck = []int32{
    0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x0c, 0x0b, 0x0d, //方块 A234567 QJK
    0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x1c, 0x1b, 0x1d, //黑桃
    0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27, 0x2c, 0x2b, 0x2d, //红桃
    0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x3c, 0x3b, 0x3d, //梅花
}

type GameCards struct {
    cardList []int32   //所有牌
    special  []int32   //特殊牌
    hands    [][]int32 //4个玩家手牌
}

func (g *GameCards) init() {
    g.shuffle()
    g.setSpecial()
    g.dispatchCards()
}

func (g *GameCards) shuffle() {
    cards := SliceCopy(_deck)
    g.cardList = SliceShuffle(cards)
}

func (g *GameCards) setSpecial() {
    last := g.cardList[len(g.cardList)-1]
    for k, v := range _deck {
        if v != last&0x0f {
            continue
        }
        index := -1
        if k%9 == 0 {
            index = 9 - k%10
        } else {
            index = k + 1
        }
        point := _deck[index] & 0x0f
        g.special = []int32{point, point + 0x10, point + 0x20, point + 0x30}
        break
    }
}

func (g *GameCards) dispatchCards() {
    const (
        _PlayerCnt = 4
        _DealCnt   = 3
    )

    start := 0
    for i := 0; i < _PlayerCnt; i++ {
        cs := g.cardList[start : start+_DealCnt]
        g.hands[i] = cs
        start += _DealCnt
    }
}
