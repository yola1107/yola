package calc

import (
	"sort"
)

// CardType 牌型定义
type CardType int

const (
	CARD_TYPE_ERROR   CardType = iota // 错误类型
	CARD_TYPE_BAOZI                   // 豹子
	CARD_TYPE_SHUNJIN                 // 顺金
	CARD_TYPE_SHUNZI                  // 顺子
	CARD_TYPE_JINHUA                  // 金花
	CARD_TYPE_DUIZI                   // 对子
	CARD_TYPE_DANPAI                  // 单牌
)

// Card 结构体
type Card struct {
	Value int
	Suit  int
}

type tagCombine struct {
	ty    CardType
	cards []int
}

func DebugCard(deck []int, combinationArray []CardType) []int {
	// 转换为 Card 类型
	cards := intToCards(deck)

	// 获取所有可能的整数形式
	result := findCombinations(cards, combinationArray)

	genCards := []int{}
	for _, v := range result {
		genCards = append(genCards, v.cards...)
	}

	// 生成新的卡牌堆 调整堆中的卡牌顺序
	heap := make([]int, len(deck))
	copy(heap, deck)
	for idx := 0; idx < len(genCards); idx++ {
		if heap[idx] != genCards[idx] {
			for j := idx + 1; j < len(heap); j++ {
				if heap[j] == genCards[idx] {
					heap[idx], heap[j] = heap[j], heap[idx]
					break
				}
			}
		}
	}

	// 返回调整后的牌堆
	return heap
}

// 主函数，按组合数组快速找到组合
func findCombinations(deck []Card, combinationArray []CardType) []tagCombine {
	results := make([]tagCombine, 0)
	usedCards := make(map[int]bool) // 全局使用的牌

	for _, combination := range combinationArray {
		// 使用递归生成组合
		result := findCombination(deck, combination, []Card{}, 0, usedCards)
		if result != nil {
			results = append(results, tagCombine{
				ty:    combination,
				cards: cardsToInt(result),
			})
			// 将使用的牌标记为已用
			for _, card := range result {
				for i, deckCard := range deck {
					if deckCard == card {
						usedCards[i] = true
						break
					}
				}
			}
		}
	}

	return results
}

// 高效递归生成组合，避免重复组合和提前剪枝
func findCombination(deck []Card, combinationType CardType, currentCombo []Card, startIndex int, usedCards map[int]bool) []Card {
	if len(currentCombo) == 3 {
		// 当组合达到3张牌时，进行验证
		if isValidCombination(currentCombo, combinationType) {
			return currentCombo
		}
		return nil
	}

	for i := startIndex; i < len(deck); i++ {
		// 如果当前牌已经使用过，跳过
		if usedCards[i] {
			continue
		}

		// 加入当前牌，递归下一步
		usedCards[i] = true
		result := findCombination(deck, combinationType, append(currentCombo, deck[i]), i+1, usedCards)
		if result != nil {
			return result
		}
		// 回溯，撤销使用的牌
		usedCards[i] = false
	}
	return nil
}

// 判断是否为顺子
func isStraight(cards []Card) bool {
	values := make([]int, len(cards))
	for i, card := range cards {
		if card.Value == 1 {
			values[i] = 0x0d + 1
		} else {
			values[i] = card.Value
		}
	}
	sort.Ints(values)
	for i := 1; i < len(values); i++ {
		if values[i] != values[i-1]+1 {
			return false
		}
	}
	return true
}

// 判断是否为同花顺
func isFlushStraight(cards []Card) bool {
	return isFlush(cards) && isStraight(cards)
}

// 判断是否为对子
func isPair(cards []Card) bool {
	return (cards[0].Value == cards[1].Value && cards[0].Value != cards[2].Value) ||
		(cards[1].Value == cards[2].Value && cards[1].Value != cards[0].Value) ||
		(cards[0].Value == cards[2].Value && cards[0].Value != cards[1].Value)
}

// 判断是否为豹子
func isTriplet(cards []Card) bool {
	return cards[0].Value == cards[1].Value && cards[1].Value == cards[2].Value
}

// 判断是否为同花
func isFlush(cards []Card) bool {
	return cards[0].Suit == cards[1].Suit && cards[1].Suit == cards[2].Suit
}

// 判断当前牌型是否符合要求
func isValidCombination(cards []Card, combinationType CardType) bool {
	switch combinationType {
	case CARD_TYPE_SHUNZI:
		return isStraight(cards) && !isFlush(cards)
	case CARD_TYPE_SHUNJIN:
		return isFlushStraight(cards)
	case CARD_TYPE_DUIZI:
		return isPair(cards)
	case CARD_TYPE_BAOZI:
		return isTriplet(cards)
	case CARD_TYPE_JINHUA:
		return isFlush(cards) && !isStraight(cards)
	case CARD_TYPE_DANPAI:
		// 确保三张牌的值都不同，且不是顺子、对子、豹子、金花或顺金
		if cards[0].Value != cards[1].Value && cards[0].Value != cards[2].Value && cards[1].Value != cards[2].Value {
			return !isInvalidCombination(cards)
		}

	case CARD_TYPE_ERROR:
		return true //错误类型不校验牌型了,取三张
	}
	return false
}

// 判断是否是无效的组合
func isInvalidCombination(cards []Card) bool {
	return isStraight(cards) || isPair(cards) || isTriplet(cards) || isFlush(cards) || isFlushStraight(cards)
}

// 将整数转换为 Card 类型
func intToCards(deck []int) []Card {
	cards := make([]Card, len(deck))
	for i, val := range deck {
		value := val & 0x0f       // 获取牌值的低四位
		suit := (val & 0xf0) >> 4 // 获取花色的高四位
		cards[i] = Card{Value: value, Suit: suit}
	}
	return cards
}

// 将 Card 转换为整数类型
func cardsToInt(cards []Card) []int {
	ret := make([]int, len(cards))
	for i, v := range cards {
		ret[i] = v.Value | v.Suit<<4
	}
	return ret
}
