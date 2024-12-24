package calc

import (
	"fmt"
	"sort"
)

// Constants
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

	data, uniqueCards := convertToCardNodes(cards)
	sort.Slice(uniqueCards, func(i, j int) bool {
		return uniqueCards[i] < uniqueCards[j]
	})

	memo := make(map[string]bool)
	backtracking(data, uniqueCards, 0, result, nil, maxNum, laiZi, memo)
	return result
}

// convertToCardNodes converts hand cards to a structured format.
func convertToCardNodes(cList []int32) ([4][14]*Node, []int32) {
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
func backtracking(data [4][14]*Node, uniqueCards []int32, index int, result *TagResult, cache []ComInfo, maxNum, laiZi int, memo map[string]bool) {
	// 剪枝：如果当前结果已达到最大可能值，提前返回
	if result.Value >= maxNum {
		return
	}

	// 更新当前结果
	currentValue := calculateCacheValue(cache)
	if currentValue > result.Value {
		result.Value = currentValue
		result.Info = append([]ComInfo{}, cache...) // Copy current cache
	}

	// 状态缓存
	//stateKey := generateStateKey(data, uniqueCards, laiZi)
	stateKey := fmt.Sprintf("(%+v_%+v)", index, laiZi)
	if memo[stateKey] {
		return // Avoid reprocessing the same state
	}
	memo[stateKey] = true
	//fmt.Printf("%+v\n", stateKey)

	// 遍历卡牌，从当前索引开始
	for k := index; k < len(uniqueCards); k++ {
		cardType, cardValue := uniqueCards[k]/CardTypeMask, uniqueCards[k]%CardTypeMask
		if data[cardType][cardValue].Count > 0 {

			formGroups(data, uniqueCards[k:], k, result, cache, maxNum, laiZi, cardValue, memo)         // 尝试构建组
			fromSequence(data, uniqueCards, k, result, cache, maxNum, laiZi, cardType, cardValue, memo) // 尝试构建顺子
		}
	}
	//fmt.Printf("----------%+v\n", stateKey)
}

// formGroups attempts to build groups from the given card.
func formGroups(data [4][14]*Node, uniqueCards []int32, index int, result *TagResult, cache []ComInfo, maxNum, laiZi int, cardValue int32, memo map[string]bool) {
	for length := GroupMinLength; length <= GroupMaxLength; length++ {
		tag := buildGroup(data, laiZi, cardValue, length)
		if len(tag.sequence) > 0 && tag.replaceCount <= laiZi {
			modifyCardCounts(data, tag.sequence, false)
			cache = append(cache, ComInfo{Value: tag.sequence, Type: 0, Replace: tag.replace})

			backtracking(data, uniqueCards, index+1, result, cache, maxNum, laiZi-tag.replaceCount, memo)

			cache = cache[:len(cache)-1] // Backtrack
			modifyCardCounts(data, tag.sequence, true)
		}
	}
}

// buildGroup constructs a group of cards based on given parameters.
func buildGroup(data [4][14]*Node, laiZi int, cardValue int32, length int) (tag tagSequence) {
	cnt := data[0][cardValue].Count + data[1][cardValue].Count + data[2][cardValue].Count + data[3][cardValue].Count + laiZi
	if !(cnt >= GroupMinLength && cnt >= length) {
		return // Not enough cards to form a valid group
	}

	cList := []int32{CardTypeMask*0 + cardValue, CardTypeMask*1 + cardValue, CardTypeMask*2 + cardValue, CardTypeMask*3 + cardValue}
	sort.Slice(cList, func(i, j int) bool {
		return data[cList[i]/CardTypeMask][cardValue].Count > data[cList[j]/CardTypeMask][cardValue].Count
	})

	sequence, replace := make([]int32, 0, length), make([]int32, 0)
	for i := 0; i < length; i++ {
		card := cList[i]
		sequence = append(sequence, card)
		if data[card/CardTypeMask][cardValue].Count <= 0 {
			replace = append(replace, card)
		}
	}
	return tagSequence{sequence: sequence, replace: replace, replaceCount: len(replace)}
}

// fromSequence attempts to build sequences from the given card.
func fromSequence(data [4][14]*Node, uniqueCards []int32, index int, result *TagResult, cache []ComInfo, maxNum, laiZi int, cardType, cardValue int32, memo map[string]bool) {
	for _, forward := range []bool{true, false} {
		tags := buildSequence(data, laiZi, maxNum, cardType, cardValue, forward)
		for _, tag := range tags {
			if tag.replaceCount <= laiZi && len(tag.sequence) >= SequenceMinLength && len(tag.sequence) <= SequenceMaxLength {
				modifyCardCounts(data, tag.sequence, false)
				cache = append(cache, ComInfo{Value: tag.sequence, Type: 1, Replace: tag.replace})

				backtracking(data, uniqueCards, index+1, result, cache, maxNum, laiZi-tag.replaceCount, memo)

				cache = cache[:len(cache)-1] // Backtrack
				modifyCardCounts(data, tag.sequence, true)
			}
		}
	}
}

// buildSequence constructs sequences of cards based on given parameters.
func buildSequence(data [4][14]*Node, laiZi, maxNum int, cardType, cardValue int32, forward bool) []tagSequence {
	neededWildcards := 0
	maxSeq := make([]int32, 0)

	start, end, step := cardValue, int32(MaxCardValue), int32(1)
	if !forward {
		start, end, step = cardValue, MinCardValue, int32(-1)
	}

	for tv := start; (forward && tv <= end) || (!forward && tv >= end); tv += step {
		if tv < MinCardValue || tv > MaxCardValue {
			break
		}

		if data[cardType][tv].Count > 0 {
			maxSeq = append(maxSeq, tv)
		} else if neededWildcards < laiZi {
			maxSeq = append(maxSeq, tv)
			neededWildcards++
		} else {
			break
		}
	}

	if len(maxSeq) < SequenceMinLength {
		return nil // Not enough cards to form a sequence
	}

	sequences := make([]tagSequence, 0)
	for j := len(maxSeq); j >= SequenceMinLength; j-- {
		sequence, replace := make([]int32, j), make([]int32, 0)
		replaceCount := 0

		for i := 0; i < j; i++ {
			tv := maxSeq[i]
			card := CardTypeMask*cardType + tv
			sequence[i] = card
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

// 生成状态缓存的唯一键
func generateStateKey(data [4][14]*Node, unique []int32, laiZi int) string {
	key := fmt.Sprintf("%d~", laiZi)
	for _, card := range unique {
		cardType, cardValue := card/CardTypeMask, card%CardTypeMask
		if count := data[cardType][cardValue].Count; count > 0 {
			key += fmt.Sprintf("[%d:%d] ", card, count)
		}
	}
	return key
}
