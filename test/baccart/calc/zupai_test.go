package calc

import (
	"fmt"
	"math/rand"
	"testing"
	"time"
)

func TestZuPai(t *testing.T) {
	a := []int{
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c,
		0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18, 0x19, 0x1a, 0x1b, 0x1c,
		0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27, 0x28, 0x29, 0x2a, 0x2b, 0x2c,
		0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, 0x3a, 0x3b, 0x3c,
	}
	a = SliceShuffle(a)

	r := findCombinations(intToCards(a), PairBankWin)
	fmt.Printf("a:%+v \n", r)

	fmt.Printf("b:%+v p:%+v\n", calculatePoints(intToCards(r[0])), calculatePoints(intToCards(r[1])))

}
func SliceShuffle(slice []int) []int {
	for i := len(slice) - 1; i > 0; i-- {
		j := rand.Intn(i + 1)
		slice[i], slice[j] = slice[j], slice[i]
	}
	return slice
}

func TestZuPai2(t *testing.T) {

	var (
		count = 0

		use = float32(0)

		MaxUse = float32(0)

		overCnt = 0

		lessCnt = 0

		maxRet [][]int

		deck = []int{
			0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, // 1-13
			0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18, 0x19, 0x1a, 0x1b, 0x1c, 0x1d, // 1-13
			0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27, 0x28, 0x29, 0x2a, 0x2b, 0x2c, 0x2d, // 1-13
			0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, 0x3a, 0x3b, 0x3c, 0x3d, // 1-13
		}

		conditionList = []CardType{
			BankerWin,     // 庄家赢
			PlayerWin,     // 闲家赢
			Equal,         // 平局
			PairBankWin,   //
			PairPlayerWin, //
		}
	)

	// 将整数牌值转换为 Card 结构体
	cards := intToCards(deck)

	for i := 0; i < 10000; i++ {

		for _, condition := range conditionList {

			count++

			s := time.Now().UnixNano() / 1e3

			ret := findCombinations(cards, condition)

			e := time.Now().UnixNano() / 1e3
			u := float32(e-s) / 1000

			if u > MaxUse {
				MaxUse = u
				maxRet = ret
			}

			if u < float32(1) {
				lessCnt++
			}
			if u > float32(2) {
				overCnt++
			}
			use += u
		}

	}

	fmt.Printf("MAX: %+v\n", maxRet)
	fmt.Printf("总次数:%+v 总耗时：%0.3fms 平均耗时:%0.3fms \n"+
		"最长耗时:%0.3fms (>2ms):%+v  (<1ms):%+v\n", count, use, use/float32(count), MaxUse, overCnt, lessCnt)
}
