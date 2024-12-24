package calc

import (
	"fmt"
	"math/rand"
	"testing"
	"time"
)

var CardList = []int32{
	0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, // 1-13 红
	0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18, 0x19, 0x1a, 0x1b, 0x1c, 0x1d, // 1-13 黄
	0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27, 0x18, 0x29, 0x2a, 0x2b, 0x2c, 0x2d, // 1-13 蓝
	0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, 0x3a, 0x3b, 0x3c, 0x3d, // 1-13 黑
	//0x4d, 0x4d,
}

func TestZuPai(t *testing.T) {

	start := time.Now().UnixNano() / 1e3

	laiZi := 1

	//hand := []int32{9, 2, 3, 4, 4, 5, 5, 6, 6, 6, 6, 7} // [2,3,4,5] [4,5,6] [6,6,6] [9]
	//hand := []int32{1, 0x06, 0x16, 0x26}
	hand := []int32{0x09, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x14, 0x15, 0x16, 0x26, 0x36}
	//hand := []int32{0x09, 0x02, 0x03, 0x03, 0x04, 0x04, 0x05, 0x05, 0x06}
	//hand := []int32{0x09, 0x03, 0x04, 0x04, 0x05, 0x05, 0x06}
	//hand := []int32{0x06, 0x05, 0x04}
	//hand := []int32{0x16, 0x15, 0x14}
	//hand := []int32{0x16, 0x14}
	//hand := []int32{0x16, 0x17, 0x18}
	//hand := []int32{0x1c}

	fmt.Printf("lai:%+v cnt:%+v card:%+v\n", laiZi, len(hand), hand)

	res := Permute(hand, laiZi)
	fmt.Printf("%+v\n", res)

	end := time.Now().UnixNano() / 1e3
	fmt.Printf("耗时：%0.3fms\n", float32(end-start)/1000)

	fmt.Printf("%+v\n", check(hand, laiZi, res))

}

func TestZuPai2(t *testing.T) {

	var (
		count = 0

		MaxNum = 14

		use = float32(0)

		MaxUse = float32(0)

		overCnt = 0

		lessCnt = 0
	)

	for i := 0; i < 2000; i++ {
		for laiZi := 0; laiZi <= 4; laiZi++ {

			deck := SliceShuffle(CardList)
			hand := deck[:MaxNum-laiZi]
			s := time.Now().UnixNano() / 1e3

			count++
			res := Permute(hand, laiZi)

			e := time.Now().UnixNano() / 1e3
			u := float32(e-s) / 1000

			if u > MaxUse {
				MaxUse = u
			}

			if u < float32(1) {
				lessCnt++
			}
			if u > float32(2) {
				overCnt++
			}
			use += u

			if !check(hand, laiZi, res) {
				fmt.Printf("=====err, hand:%+v lai:%+v res:%+v\n", hand, laiZi, res)
				return
			}
		}
	}

	fmt.Printf("总次数:%+v 总耗时：%0.3fms 平均耗时:%0.3fms \n"+
		"最长耗时:%0.3fms (>2ms):%+v  (<1ms):%+v\n", count, use, use/float32(count), MaxUse, overCnt, lessCnt)
}

func SliceShuffle(slice []int32) []int32 {
	for i := len(slice) - 1; i > 0; i-- {
		j := rand.Intn(i + 1)
		slice[i], slice[j] = slice[j], slice[i]
	}
	return slice
}

func check(hand []int32, lai int, res *TagResult) (ok bool) {
	if res.Value > len(hand)+lai {
		return false
	}

	m := map[int32]int{}
	for _, v := range hand {
		m[v]++
	}

	m2 := map[int32]int{}
	for _, v := range res.Info {
		for _, c := range v.Value {
			m2[c]++
		}
	}

	mis := 0
	// 保证m2 都在m中 m中没有的计数mis
	for c, n2 := range m2 {
		n := m[c]
		if n < n2 {
			mis += n2 - n
			continue
		}
	}

	if mis > lai {
		return false
	}

	return true
}
