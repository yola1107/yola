package calc1

import (
    "sort"
)

const (
    CardTypeError   int = 0 // 错误类型
    CardTypeBaoZi   int = 1 // 豹子
    CardTypeShunJin int = 2 // 顺金
    CardTypeShunZi  int = 3 // 顺子
    CardTypeJinHua  int = 4 // 金花
    CardTypeDui     int = 5 // 对子
    CardTypeSingle  int = 6 // 单牌
    CardTypeRandom  int = 7 // 随机
)

type tagResult struct {
    count     int
    condition []int
    value     []int
    info      []comInfo // 组合，顺序与 condition 对应
}

type tagComInfo struct {
    incr map[int]int // 牌型计数
    info []comInfo   // 组合
}

type comInfo struct {
    cardType int   // 牌型
    value    []int // 牌值
}

// DebugCard 配牌
func DebugCard(cards []int, condition []int) []int {
    if len(cards) == 0 {
        return cards
    }

    ret := permute(cards, condition)
    if ret == nil || len(ret.value) == 0 {
        return cards
    }

    for idx := 0; idx < len(ret.value); idx++ {
        if ret.value[idx] != cards[idx] {
            for j := idx + 1; j < len(cards); j++ {
                if ret.value[idx] == cards[j] {
                    cards[idx], cards[j] = cards[j], cards[idx]
                    break
                }
            }
        }
    }

    return cards
}

func permute(cards []int, condition []int) *tagResult {
    m := make(map[int]int, len(cards))
    scoreMap := make(map[int][]int)
    colorMap := make(map[int][]int)

    for _, card := range cards {
        m[card]++
        score := GetCardScore(card)
        color := GetCardColor(card)
        scoreMap[score] = append(scoreMap[score], card)
        colorMap[color] = append(colorMap[color], card)
    }

    conditionCount := make(map[int]int, len(condition))
    for _, v := range condition {
        conditionCount[v]++
    }

    res := &tagResult{condition: condition}
    cache := &tagComInfo{incr: make(map[int]int)}

    backtracking(m, conditionCount, len(condition), scoreMap, colorMap, res, cache)

    return res
}

func backtracking(m map[int]int, condition map[int]int, maxComNum int, scoreMap, colorMap map[int][]int, res *tagResult, cache *tagComInfo) {
    if res.count > 0 {
        return
    }

    currentValue := len(cache.info)
    if currentValue >= maxComNum {
        res.count++
        res.info = make([]comInfo, len(cache.info))
        copy(res.info, cache.info)

        for idx, ty := range res.condition {
            if res.info[idx].cardType != ty {
                for j := idx + 1; j < len(res.info); j++ {
                    if res.info[j].cardType == ty {
                        res.info[idx], res.info[j] = res.info[j], res.info[idx]
                        break
                    }
                }
            }
        }

        for _, v := range res.info {
            res.value = append(res.value, v.value...)
        }
        return
    }

    for k, v := range m {
        if v <= 0 {
            continue
        }

        for ty := CardTypeBaoZi; ty <= CardTypeSingle+1; ty++ {
            if condition[ty] > 0 && condition[ty] > cache.incr[ty] {
                groups := calcGroupsByType(m, scoreMap, colorMap, k, ty)
                for _, group := range groups {
                    cache.incr[ty]++
                    cache.info = append(cache.info, comInfo{value: group, cardType: ty})
                    modifyCardCounts(m, group, false)

                    backtracking(m, condition, maxComNum, scoreMap, colorMap, res, cache)

                    cache.incr[ty]--
                    cache.info = cache.info[:len(cache.info)-1]
                    modifyCardCounts(m, group, true)
                }
            }
        }
    }
}

func calcGroupsByType(m map[int]int, scoreMap, colorMap map[int][]int, card int, cardType int) [][]int {
    score := GetCardScore(card)
    color := GetCardColor(card)

    switch cardType {
    case CardTypeBaoZi:
        if cList := scoreMap[score]; len(cList) >= 3 && m[cList[0]] > 0 && m[cList[1]] > 0 && m[cList[2]] > 0 {
            return [][]int{{cList[0], cList[1], cList[2]}}
        }
    case CardTypeDui:
        if cList := scoreMap[score]; len(cList) >= 2 && m[cList[0]] > 0 && m[cList[1]] > 0 {
            for c1 := range m {
                if m[c1] > 0 && score != GetCardScore(c1) {
                    return [][]int{{cList[0], cList[1], c1}}
                }
            }
        }
    case CardTypeShunZi:
        if cList1, cList2 := scoreMap[score+1], scoreMap[score+2]; len(cList1) > 0 && len(cList2) > 0 {
            for _, c1 := range cList1 {
                for _, c2 := range cList2 {
                    if m[card] > 0 && m[c1] > 0 && m[c2] > 0 && len(uniqueColors(card, c1, c2)) > 1 {
                        return [][]int{{card, c1, c2}}
                    }
                }
            }
        }
    case CardTypeShunJin:
        if score == 14 {
            c1, c2 := card/0x10*color+12, card/0x10*color+13
            if m[card] > 0 && m[c1] > 0 && m[c2] > 0 {
                return [][]int{{card, c1, c2}}
            }
        }
        if m[card] > 0 && m[card+1] > 0 && m[card+2] > 0 {
            return [][]int{{card, card + 1, card + 2}}
        }

    case CardTypeJinHua:
        colorCards := colorMap[color]
        if len(colorCards) < 3 {
            return nil
        }
        for _, c1 := range colorCards {
            for _, c2 := range colorCards {
                if m[c1] <= 0 || m[c2] <= 0 || m[card] <= 0 || c1 == c2 || c1 == card || c2 == card {
                    continue
                }
                if group := []int{card, c1, c2}; !isConsecutive(group) {
                    return [][]int{group}
                }
            }
        }
    case CardTypeSingle:
        for c1 := range m {
            for c2 := range m {
                if c1 != c2 && card != c1 && card != c2 && m[card] > 0 && m[c1] > 0 && m[c2] > 0 {
                    group := []int{card, c1, c2}
                    if len(uniqueColors(group...)) > 1 && !isConsecutive(group) {
                        return [][]int{group}
                    }
                }
            }
        }
    case CardTypeRandom:
        group := []int{}
        for k, v := range m {
            if v <= 0 {
                continue
            }
            group = append(group, k)
            if len(group) == 3 {
                return [][]int{group}
            }
        }
    }
    return nil
}

func modifyCardCounts(m map[int]int, group []int, restore bool) {
    for _, card := range group {
        if restore {
            m[card]++
        } else {
            m[card]--
        }
    }
}

func uniqueColors(cards ...int) map[int]struct{} {
    colors := make(map[int]struct{})
    for _, card := range cards {
        colors[GetCardColor(card)] = struct{}{}
    }
    return colors
}

func isConsecutive(cards []int) bool {
    sort.Slice(cards, func(i, j int) bool {
        return GetCardScore(cards[i]) < GetCardScore(cards[j])
    })
    for i := 1; i < len(cards); i++ {
        if GetCardScore(cards[i]) != GetCardScore(cards[i-1])+1 {
            return false
        }
    }
    return true
}

func GetCardColor(card int) int {
    return card / 0x10
}

func GetCardScore(card int) int {
    num := GetCardNum(card)
    // A>K
    if num == 1 {
        num = 14
    }
    return num
}

func GetCardNum(card int) int {
    return card % 0x10
}
