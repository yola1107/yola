package calc7

import (
    "fmt"
    "sort"
)

// 常量定义
const (
    GroupMinLength    = 3  // 组最小长度
    GroupMaxLength    = 4  // 组最大长度
    SequenceMinLength = 3  // 顺子最小长度
    SequenceMaxLength = 13 // 顺子最大长度
    MinCardValue      = 1  // 最小牌值
    MaxCardValue      = 13 // 最大牌值
    CardTypeMask      = 0x10
)

// TagResult holds the maximum value and combinations found.
type TagResult struct {
    Value int       // Maximum value
    Info  []ComInfo // Combinations
}

// ComInfo contains information about a combination.
type ComInfo struct {
    Type    int32   // 0: Group, 1: Sequence
    Value   []int32 // Card values
    Replace []int32 // Replacement values for wildcards
}

// Node represents a card and its count.
type Node struct {
    Card  int32 // Card value
    Count int   // Count of this card
}

// tagSequence holds a sequence and its replacement information.
type tagSequence struct {
    sequence     []int32
    replace      []int32
    replaceCount int
}

// Permute initializes the backtracking process to find valid card combinations.
func Permute(cards []int32, laiZi int) *TagResult {
    result := &TagResult{}
    maxNum := len(cards) + laiZi
    if maxNum < GroupMinLength {
        return result // Not enough cards to form a group
    }

    data, cList := hCardToDCard(cards)
    sort.Slice(cList, func(i, j int) bool {
        return cList[i] < cList[j]
    })

    // 定义状态缓存
    memo := make(map[string]bool)

    backtracking(data, cList, 0, result, nil, maxNum, laiZi, memo)
    return result
}

// hCardToDCard converts hand cards to a structured format.
func hCardToDCard(cList []int32) ([4][14]*Node, []int32) {
    var data [4][14]*Node
    uniqueCardsMap := make(map[int32]struct{})
    uniqueCards := []int32{}

    // Initialize the data structure for card nodes.
    for i := range data {
        for j := range data[i] {
            data[i][j] = &Node{}
        }
    }

    // Populate data and track unique cards.
    for _, card := range cList {
        cardType, cardValue := card/CardTypeMask, card%CardTypeMask
        if cardType < 0 || cardType >= 4 || cardValue < MinCardValue || cardValue > MaxCardValue {
            continue
        }
        data[cardType][cardValue].Card = card
        data[cardType][cardValue].Count++
        if _, exists := uniqueCardsMap[card]; !exists {
            uniqueCardsMap[card] = struct{}{}
            uniqueCards = append(uniqueCards, card)
        }
    }

    return data, uniqueCards
}

// backtracking explores all valid combinations recursively.
func backtracking(data [4][14]*Node, cList []int32, index int, result *TagResult, cache []ComInfo, maxNum, laiZi int, memo map[string]bool) {
    // 剪枝：如果当前分数已达最大可能值，提前退出
    if result.Value >= maxNum {
        return // Early exit if optimal result is found
    }

    // 更新结果
    currentValue := calculateCacheValue(cache)
    if currentValue > result.Value {
        result.Value = currentValue
        result.Info = append([]ComInfo{}, cache...) // Copy current cache
    }

    // 状态缓存
    //stateKey := generateStateKey(data, cList, laiZi)
    stateKey := fmt.Sprintf("(%+v_%+v)", index, laiZi)
    if memo[stateKey] {
        return
    }
    memo[stateKey] = true
    //fmt.Printf("%+v\n", stateKey)

    // Explore combinations starting from the current index.
    for k := index; k < len(cList); k++ {
        cardType, cardValue := cList[k]/CardTypeMask, cList[k]%CardTypeMask
        if data[cardType][cardValue].Count > 0 {
            formGroups(data, cList[k:], k, result, cache, maxNum, laiZi, cardValue, memo)
            fromSequence(data, cList, k, result, cache, maxNum, laiZi, cardType, cardValue, memo)
        }
    }

    //fmt.Printf("----------%+v\n", stateKey)
}

// formGroups attempts to build groups from the given card.
func formGroups(data [4][14]*Node, cList []int32, index int, result *TagResult, cache []ComInfo, maxNum, laiZi int, cardValue int32, memo map[string]bool) {

    //判断合法长度
    if cnt := data[0][cardValue].Count + data[1][cardValue].Count + data[2][cardValue].Count + data[3][cardValue].Count + laiZi; cnt < GroupMinLength {
        return
    }

    groups := [][]int32{
        {CardTypeMask*0 + cardValue, CardTypeMask*1 + cardValue, CardTypeMask*2 + cardValue},
        {CardTypeMask*0 + cardValue, CardTypeMask*1 + cardValue, CardTypeMask*3 + cardValue},
        {CardTypeMask*0 + cardValue, CardTypeMask*2 + cardValue, CardTypeMask*3 + cardValue},
        {CardTypeMask*1 + cardValue, CardTypeMask*2 + cardValue, CardTypeMask*3 + cardValue},
        {CardTypeMask*0 + cardValue, CardTypeMask*1 + cardValue, CardTypeMask*2 + cardValue, CardTypeMask*3 + cardValue},
    }

    for _, group := range groups {

        totalCount := data[0][cardValue].Count + data[1][cardValue].Count +
                data[2][cardValue].Count + data[3][cardValue].Count + laiZi

        if len(group) > totalCount {
            continue
        }

        replace, replaceCount := prepareReplacement(data, group)
        if replaceCount <= laiZi {
            modifyCardCounts(data, group, false)
            cache = append(cache, ComInfo{Value: group, Type: 0, Replace: replace})

            backtracking(data, cList, index+1, result, cache, maxNum, laiZi-replaceCount, memo)

            cache = cache[:len(cache)-1]
            modifyCardCounts(data, group, true)
        }
    }
}

// fromSequence attempts to build sequences from the given card.
func fromSequence(data [4][14]*Node, cList []int32, index int, result *TagResult, cache []ComInfo, maxNum, laiZi int, cardType, cardValue int32, memo map[string]bool) {
    for _, forward := range []bool{true, false} {
        tags := buildSequence(data, laiZi, maxNum, cardType, cardValue, forward)
        for _, tag := range tags {
            cnt := len(tag.sequence)
            if tag.replaceCount <= laiZi && cnt >= SequenceMinLength && cnt <= SequenceMaxLength {
                modifyCardCounts(data, tag.sequence, false) // Deduct counts for the sequence
                cache = append(cache, ComInfo{Value: tag.sequence, Type: 1, Replace: tag.replace})

                nextIndex := index
                if data[cardType][cardValue].Count <= 0 {
                    nextIndex++
                }
                backtracking(data, cList, nextIndex, result, cache, maxNum, laiZi-tag.replaceCount, memo)

                // Backtrack
                cache = cache[:len(cache)-1]
                modifyCardCounts(data, tag.sequence, true) // Restore counts
            }
        }
    }
}

// buildSequence constructs sequences of cards based on given parameters.
func buildSequence(data [4][14]*Node, laiZi, maxNum int, cardType, cardValue int32, forward bool) []tagSequence {
    need := 0
    maxSeq := []int32{}

    // Build a maximum sequence of available cards.
    var tvStart, tvEnd, step int32
    if forward {
        tvStart, tvEnd, step = cardValue, MaxCardValue, 1
    } else {
        tvStart, tvEnd, step = cardValue, MinCardValue, -1
    }

    for tv := tvStart; (forward && tv <= tvEnd) || (!forward && tv >= tvEnd); tv += step {
        if tv < MinCardValue || tv > MaxCardValue {
            break
        }

        if data[cardType][tv].Count > 0 {
            maxSeq = append(maxSeq, tv)
        } else if need < laiZi {
            maxSeq = append(maxSeq, tv)
            need++
        } else {
            break
        }
    }

    if len(maxSeq) < SequenceMinLength {
        return nil // Early exit if not enough cards to form a sequence
    }

    sequences := []tagSequence{}
    // Create sequences from the maximum sequence found.
    for j := len(maxSeq); j >= SequenceMinLength; j-- {
        sequence, replace, replaceCount := []int32{}, []int32{}, 0

        for i := 0; i < j; i++ {
            tv := maxSeq[i]
            card := CardTypeMask*cardType + tv
            sequence = append(sequence, card)
            if data[cardType][tv].Count <= 0 {
                replace = append(replace, card)
                replaceCount++
            }
        }

        sequences = append(sequences, tagSequence{
            sequence:     sequence,
            replace:      replace,
            replaceCount: replaceCount,
        })
    }

    return sequences
}

// prepareReplacement identifies which cards need to be replaced.
func prepareReplacement(data [4][14]*Node, group []int32) ([]int32, int) {
    replace := []int32{}
    replaceCount := 0

    for _, card := range group {
        cardType, cardValue := card/CardTypeMask, card%CardTypeMask
        if data[cardType][cardValue].Count <= 0 {
            replace = append(replace, card)
            replaceCount++
        }
    }
    return replace, replaceCount
}

// modifyCardCounts adjusts the count of cards in the data structure.
func modifyCardCounts(data [4][14]*Node, group []int32, restore bool) {
    for _, card := range group {
        cardType, cardValue := card/CardTypeMask, card%CardTypeMask
        if restore {
            data[cardType][cardValue].Count++
        } else {
            data[cardType][cardValue].Count--
        }
    }
}

// calculateCacheValue computes the total value from the cache.
func calculateCacheValue(cache []ComInfo) int {
    total := 0
    for _, v := range cache {
        total += len(v.Value)
    }
    return total
}

// 生成状态键
func generateStateKey(data [4][14]*Node, unique []int32, laiZi int) string {
    key := fmt.Sprintf("%d~", laiZi)

    for _, card := range unique {
        cardType, cardValue := card/CardTypeMask, card%CardTypeMask
        key += fmt.Sprintf("[%d:%d] ", card, data[cardType][cardValue].Count)
    }

    return key
}
