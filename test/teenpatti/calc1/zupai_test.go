package calc1

import (
    "fmt"
    "math/rand"
    "testing"
    "time"
)

func TestZuPai(t *testing.T) {

    var (
        start = time.Now().UnixNano() / 1e3

        condition = []int{CardTypeBaoZi, CardTypeRandom, CardTypeShunZi, CardTypeRandom, CardTypeRandom}
        //condition = []int{CardTypeBaoZi, CardTypeBaoZi, CardTypeShunZi, CardTypeShunJin, CardTypeBaoZi}

        cards = []int{
            0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, // 1-13
            0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18, 0x19, 0x1a, 0x1b, 0x1c, 0x1d, // 1-13
            0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27, 0x18, 0x29, 0x2a, 0x2b, 0x2c, 0x2d, // 1-13
            0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, 0x3a, 0x3b, 0x3c, 0x3d, // 1-13
        }
    )

    res := permute(cards, condition)
    end := time.Now().UnixNano() / 1e3

    fmt.Printf("res:%+v\n", res)
    fmt.Printf("耗时：%0.3fms\n", float32(end-start)/1000)
}

func TestDebugCard(t *testing.T) {
    var (
        start = time.Now().UnixNano() / 1e3

        condition = []int{CardTypeBaoZi, CardTypeBaoZi, CardTypeShunZi, CardTypeShunJin, CardTypeBaoZi}

        cards = []int{
            0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, // 1-13
            0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18, 0x19, 0x1a, 0x1b, 0x1c, 0x1d, // 1-13
            0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27, 0x18, 0x29, 0x2a, 0x2b, 0x2c, 0x2d, // 1-13
            0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, 0x3a, 0x3b, 0x3c, 0x3d, // 1-13
        }
    )

    cs := DebugCard(cards, condition)

    ok := checkArrayEqual(cs, cards)

    end := time.Now().UnixNano() / 1e3

    fmt.Printf("res:%+v\n%+v\n%+v\n", ok, condition, cs)
    fmt.Printf("耗时：%0.3fms\n", float32(end-start)/1000)
}

func checkArrayEqual(arr1, arr2 []int) bool {
    m1, m2 := map[int]int{}, map[int]int{}
    for _, v := range arr1 {
        m1[v]++
    }
    for _, v := range arr2 {
        m2[v]++
    }

    if len(m1) != len(m2) {
        return false
    }

    for k, v := range m1 {
        if v != m2[k] {
            return false
        }
    }

    return true
}

func TestZuPai2(t *testing.T) {

    var (
        count = 0

        use = float32(0)

        MaxUse = float32(0)

        overCnt = 0

        lessCnt = 0

        maxRet *tagResult

        deck = []int{
            0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, // 1-13
            0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18, 0x19, 0x1a, 0x1b, 0x1c, 0x1d, // 1-13
            0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27, 0x28, 0x29, 0x2a, 0x2b, 0x2c, 0x2d, // 1-13
            0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, 0x3a, 0x3b, 0x3c, 0x3d, // 1-13
        }

        conditionList = []int{
            CardTypeBaoZi,   //int = 1 // 豹子
            CardTypeShunJin, //int = 2 // 顺金
            CardTypeShunZi,  //int = 3 // 顺子
            CardTypeJinHua,  //int = 4 // 金花
            CardTypeDui,     //int = 5 // 对子
            CardTypeSingle,  //int = 6 // 单牌
            //CardTypeRandom, //int = 7 // 随机
        }
    )

    for i := 0; i < 1; i++ {

        for _, x1 := range conditionList {
            for _, x2 := range conditionList {
                for _, x3 := range conditionList {
                    for _, x4 := range conditionList {
                        for _, x5 := range conditionList {

                            count++

                            conditions := []int{x1, x2, x3, x4, x5}

                            s := time.Now().UnixNano() / 1e3

                            ret := permute(SliceShuffle(deck), conditions)

                            //ret2 := DebugCard(SliceShuffle(deck), conditions)

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

                            if !check(deck, ret) {
                                fmt.Printf("=====err, deck:%+v res:%+v\n", deck, ret)
                                return
                            }

                            //if ok2 := checkArrayEqual(ret2, deck); !ok2 {
                            //	fmt.Printf("=====err2, deck:%+v res:%+v\n", deck, ret)
                            //	return
                            //}
                        }
                    }
                }
            }
        }

    }
    fmt.Printf("MAX: %+v\n", maxRet)
    fmt.Printf("总次数:%+v 总耗时：%0.3fms 平均耗时:%0.3fms \n"+
            "最长耗时:%0.3fms (>2ms):%+v  (<1ms):%+v\n", count, use, use/float32(count), MaxUse, overCnt, lessCnt)
}

func SliceShuffle(slice []int) []int {
    for i := len(slice) - 1; i > 0; i-- {
        j := rand.Intn(i + 1)
        slice[i], slice[j] = slice[j], slice[i]
    }
    return slice
}

func check(deck []int, res *tagResult) (ok bool) {

    m := map[int]int{}
    for _, v := range deck {
        m[v]++
    }

    m2 := map[int]int{}
    for _, v := range res.info {
        for _, c := range v.value {
            m[c]++
        }
    }

    // 保证m2 都在m中 m中没有的计数mis
    for c, n2 := range m2 {
        n := m[c]
        if n < n2 {
            return false
        }
    }

    return true
}
