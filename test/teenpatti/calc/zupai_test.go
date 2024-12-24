package calc

import (
    "fmt"
    "math/rand"
    "testing"
    "time"
)

func TestZuPai(t *testing.T) {

    var (
        start = time.Now().UnixNano() / 1e3

        // 设置需要查找的牌型组合数组
        combinationArray = []CardType{
            CARD_TYPE_BAOZI, // 豹子
            //CARD_TYPE_BAOZI, // 豹子
            CARD_TYPE_SHUNJIN, // 顺金
            //CARD_TYPE_SHUNJIN, // 顺金
            CARD_TYPE_SHUNZI, // 顺子
            CARD_TYPE_DUIZI,  // 对子
            //CARD_TYPE_JINHUA,  // 金花
            CARD_TYPE_JINHUA, // 金花
            //CARD_TYPE_DANPAI, // 单牌
            //CARD_TYPE_DANPAI, // 单牌
            //CARD_TYPE_DANPAI, // 单牌
            //CARD_TYPE_DANPAI, // 单牌
            //CARD_TYPE_ERROR,  // 错误类型
        }

        // 使用数字表示牌
        deck = []int{
            0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, // 1-13 红桃
            0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18, 0x19, 0x1a, 0x1b, 0x1c, 0x1d, // 1-13 方块
            0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27, 0x28, 0x29, 0x2a, 0x2b, 0x2c, 0x2d, // 1-13 梅花
            0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, 0x3a, 0x3b, 0x3c, 0x3d, // 1-13 黑桃
        }
    )

    deck = SliceShuffle(deck)

    // 将整数牌值转换为 Card 结构体
    cards := make([]Card, len(deck))
    for i, val := range deck {
        value := val & 0x0f       // 获取牌值的低四位
        suit := (val & 0xf0) >> 4 // 获取花色的高四位
        cards[i] = Card{Value: value, Suit: suit}
    }

    results := findCombinations(cards, combinationArray)

    fmt.Printf("combinationArray:%+v\n", combinationArray)

    printResults(results)

    fmt.Printf("check:%+v\n", check(deck, combinationArray, results))
    fmt.Printf("耗时：%0.3fms\n", float32(time.Now().UnixNano()/1e3-start)/1000)

}

func TestZuPai2(t *testing.T) {

    var (
        count = 0

        use = float32(0)

        MaxUse = float32(0)

        overCnt = 0

        lessCnt = 0

        maxRet []tagCombine

        deck = []int{
            0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, // 1-13
            0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18, 0x19, 0x1a, 0x1b, 0x1c, 0x1d, // 1-13
            0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27, 0x28, 0x29, 0x2a, 0x2b, 0x2c, 0x2d, // 1-13
            0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, 0x3a, 0x3b, 0x3c, 0x3d, // 1-13
        }

        conditionList = []CardType{
            CARD_TYPE_BAOZI,   // 豹子
            CARD_TYPE_SHUNJIN, // 顺金
            CARD_TYPE_SHUNZI,  // 顺子
            CARD_TYPE_JINHUA,  // 金花
            CARD_TYPE_DUIZI,   // 对子
            CARD_TYPE_DANPAI,  // 单牌
        }
    )

    // 将整数牌值转换为 Card 结构体
    cards := make([]Card, len(deck))
    for i, val := range deck {
        value := val & 0x0f       // 获取牌值的低四位
        suit := (val & 0xf0) >> 4 // 获取花色的高四位
        cards[i] = Card{Value: value, Suit: suit}
    }

    for i := 0; i < 1; i++ {

        for _, x1 := range conditionList {
            for _, x2 := range conditionList {
                for _, x3 := range conditionList {
                    for _, x4 := range conditionList {
                        for _, x5 := range conditionList {

                            count++

                            conditions := []CardType{x1, x2, x3, x4, x5}

                            s := time.Now().UnixNano() / 1e3

                            //ret := permute(SliceShuffle(deck), conditions)
                            ret := findCombinations(cards, conditions)

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

                            if !check(deck, conditions, ret) {
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

func check(deck []int, combinationArray []CardType, res []tagCombine) bool {

    if len(combinationArray) != len(res) {
        return false
    }

    m := map[int]int{}
    for _, v := range deck {
        m[v]++
    }

    m2 := map[int]int{}
    for _, v := range res {
        for _, c := range v.cards {
            m2[c]++
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

// 打印结果
func printResults(results []tagCombine) {
    for _, com := range results {

        fmt.Printf("%s: [", cardTypeToString(com.ty))
        for _, card := range com.cards {
            // 格式化输出，方便阅读
            fmt.Printf("0x%x ", card) // 修正为正确格式
        }
        fmt.Println("]")

    }
}

// 将CardType转换为对应的字符串表示
func cardTypeToString(cardType CardType) string {
    switch cardType {
    case CARD_TYPE_SHUNZI:
        return "顺子(3)"
    case CARD_TYPE_SHUNJIN:
        return "顺金(2)"
    case CARD_TYPE_DUIZI:
        return "对子(5)"
    case CARD_TYPE_BAOZI:
        return "豹子(1)"
    case CARD_TYPE_JINHUA:
        return "金花(4)"
    case CARD_TYPE_DANPAI:
        return "单牌(6)"
    default:
        return "未知类型"
    }
}

func SliceShuffle(slice []int) []int {
    for i := len(slice) - 1; i > 0; i-- {
        j := rand.Intn(i + 1)
        slice[i], slice[j] = slice[j], slice[i]
    }
    return slice
}
