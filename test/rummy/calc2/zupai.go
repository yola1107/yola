package calc

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

	CardTypeMask = 0x10 // 花色掩码
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

	unique := []int32{}
	data := map[int32]int{}
	for _, card := range cards {
		data[card]++
		unique = append(unique, card)
	}

	sort.Slice(unique, func(i, j int) bool {
		return unique[i] < unique[j]
	})
	// 定义状态缓存
	memo := make(map[string]bool)

	backtracking(data, unique, result, nil, maxNum, laiZi, memo)
	return result
}

func backtracking(data map[int32]int, unique []int32, result *TagResult, cache []ComInfo, maxNum, laiZi int, memo map[string]bool) {
	// 剪枝：如果当前分数已达最大可能值，提前退出
	if result.Value >= maxNum {
		return // Early exit if optimal result is found
	}

	// 更新结果
	currentValue := calculateCacheValue(cache)
	if currentValue > result.Value {
		result.Value = currentValue
		result.Info = append([]ComInfo{}, cache...) // 深拷贝
	}

	// 状态缓存
	stateKey := generateStateKey(data, unique, laiZi)
	if memo[stateKey] {
		return
	}
	memo[stateKey] = true

	// 遍历卡牌
	for card := range data {
		if data[card] > 0 {
			cardType, cardValue := card/CardTypeMask, card%CardTypeMask
			fromGroup(data, unique, result, cache, maxNum, laiZi, cardValue, memo)
			fromSequence(data, unique, result, cache, maxNum, laiZi, cardType, cardValue, memo)
		}
	}
}

func fromGroup(data map[int32]int, unique []int32, result *TagResult, cache []ComInfo, maxNum, laiZi int, cardValue int32, memo map[string]bool) {

	// 校验组的长度
	if cnt := getCardValueCnt(data, cardValue) + laiZi; cnt < GroupMinLength {
		return
	}

	// 定义不同花色的组模板
	groupTemplates := [][]int32{
		{CardTypeMask*0 + cardValue, CardTypeMask*1 + cardValue, CardTypeMask*2 + cardValue},                             // 3 张牌
		{CardTypeMask*0 + cardValue, CardTypeMask*1 + cardValue, CardTypeMask*3 + cardValue},                             // 3 张牌
		{CardTypeMask*0 + cardValue, CardTypeMask*2 + cardValue, CardTypeMask*3 + cardValue},                             // 3 张牌
		{CardTypeMask*1 + cardValue, CardTypeMask*2 + cardValue, CardTypeMask*3 + cardValue},                             // 3 张牌
		{CardTypeMask*0 + cardValue, CardTypeMask*1 + cardValue, CardTypeMask*2 + cardValue, CardTypeMask*3 + cardValue}, // 4 张牌
	}

	for _, group := range groupTemplates {

		if cnt := getCardValueCnt(data, cardValue) + laiZi; cnt < len(group) {
			continue
		}

		replace, replaceCount := prepareReplacement(data, group)
		if replaceCount <= laiZi {
			modifyCardCounts(data, group, false)
			cache = append(cache, ComInfo{Value: group, Type: 0, Replace: replace})

			backtracking(data, unique, result, cache, maxNum, laiZi-replaceCount, memo)

			cache = cache[:len(cache)-1] // 回溯
			modifyCardCounts(data, group, true)
		}
	}
}

func fromSequence(data map[int32]int, unique []int32, result *TagResult, cache []ComInfo, maxNum, laiZi int, cardType, cardValue int32, memo map[string]bool) {
	for _, forward := range []bool{true, false} {
		tags := buildSequence(data, laiZi, cardType, cardValue, forward)
		for _, tag := range tags {
			cnt := len(tag.sequence)
			if tag.replaceCount <= laiZi && cnt >= SequenceMinLength && cnt <= SequenceMaxLength {
				modifyCardCounts(data, tag.sequence, false) // Deduct counts for the sequence
				cache = append(cache, ComInfo{Value: tag.sequence, Type: 1, Replace: tag.replace})

				backtracking(data, unique, result, cache, maxNum, laiZi-tag.replaceCount, memo)

				// Backtrack
				cache = cache[:len(cache)-1]
				modifyCardCounts(data, tag.sequence, true) // Restore counts
			}
		}
	}
}

// buildSequence constructs sequences of cards based on given parameters.
//找出所有顺子的组合 顺子从最长开始找。 forward：顺子构建中的正向和逆向遍历
func buildSequence(data map[int32]int, laiZi int, cardType, cardValue int32, forward bool) []tagSequence {

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

		card := CardTypeMask*cardType + tv
		if data[card] > 0 {
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
			if data[card] <= 0 {
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
func prepareReplacement(data map[int32]int, group []int32) ([]int32, int) {
	replace := []int32{}
	replaceCount := 0

	for _, card := range group {
		if data[card] <= 0 {
			replace = append(replace, card)
			replaceCount++
		}
	}
	return replace, replaceCount
}

// modifyCardCounts adjusts the count of cards in the data structure.
func modifyCardCounts(data map[int32]int, group []int32, restore bool) {
	for _, card := range group {
		if restore {
			data[card]++
		} else {
			data[card]--
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

func getCardValueCnt(data map[int32]int, cardValue int32) int {
	c0, c1, c2, c3 := CardTypeMask*0+cardValue, CardTypeMask*1+cardValue, CardTypeMask*2+cardValue, CardTypeMask*3+cardValue
	cnt := data[c0] + data[c1] + data[c2] + data[c3]
	return cnt
}

// 生成状态键
func generateStateKey(data map[int32]int, unique []int32, laiZi int) string {
	key := fmt.Sprintf("%d-", laiZi)
	for _, card := range unique {
		key += fmt.Sprintf("[%d:%d],", card, data[card])
	}
	return key
}
