package calc

// 牌型定义
type CardType int

// 结果常量
const (
	BankerWin     CardType = 0 // 庄家赢
	PlayerWin     CardType = 1 // 闲家赢
	Equal         CardType = 2 // 平局
	PairBankWin   CardType = 3 // 庄家对赢
	PairPlayerWin CardType = 4 // 闲家对赢
)

// Card 结构体
type Card struct {
	Value int
	Suit  int
	Num   int
}

// 主函数，按组合数组快速找到组合
func findCombinations(deck []Card, combinationType CardType) [][]int {
	result := findCombination(deck, combinationType, nil, 0, make([]bool, len(deck)))

	total := [][]int{}
	for _, v := range result {
		total = append(total, cardsToInt(v))
	}
	return total

}

// 高效递归生成组合，避免重复组合和提前剪枝
func findCombination(deck []Card, combinationType CardType, currentCombo []Card, startIndex int, usedCards []bool) [][]Card {
	// 剪枝，提前验证当前组合是否合法
	if len(currentCombo) == 2 && !isValidCombination2(currentCombo, combinationType) {
		return nil
	}
	if len(currentCombo) == 4 && !isValidCombination4(currentCombo, combinationType) {
		return nil
	}
	if len(currentCombo) == 6 {
		// 达到6张牌时进行验证
		if valid, bCard, pCard := isValidCombination6(currentCombo, combinationType); valid {
			return [][]Card{bCard, pCard}
		}
		return nil
	}

	for i := startIndex; i < len(deck); i++ {
		if usedCards[i] {
			continue
		}

		// 选择当前牌进行递归
		usedCards[i] = true
		currentCombo = append(currentCombo, deck[i])

		if result := findCombination(deck, combinationType, currentCombo, i+1, usedCards); result != nil {
			return result
		}

		// 回溯
		usedCards[i] = false
		currentCombo = currentCombo[:len(currentCombo)-1]
	}
	return nil
}

// 判断2张牌是否合法
func isValidCombination2(cards []Card, combinationType CardType) bool {
	bPair := isPair([]Card{cards[0], cards[1]})

	// 根据牌型选择合适的验证条件
	switch combinationType {
	case BankerWin:
		return !bPair
	case PlayerWin:
	case Equal:
		// 平局，无需对子验证
	case PairBankWin:
		return bPair
	case PairPlayerWin:
	}
	return true
}

// 判断4张牌是否合法
func isValidCombination4(cards []Card, combinationType CardType) bool {
	bPair, pPair := isPair([]Card{cards[0], cards[1]}), isPair([]Card{cards[2], cards[3]})

	// 根据牌型选择合适的验证条件
	switch combinationType {
	case BankerWin:
		return !bPair
	case PlayerWin:
		return !pPair
	case Equal:
		// 平局，无需对子验证
	case PairBankWin:
		return bPair
	case PairPlayerWin:
		return pPair
	}
	return true
}

// 判断当前牌型是否合法
func isValidCombination6(cards []Card, combinationType CardType) (bool, []Card, []Card) {
	bPair, pPair := isPair([]Card{cards[0], cards[1]}), isPair([]Card{cards[2], cards[3]})

	// 计算庄家和闲家的点数
	bCard, pCard := calcDrawCard(cards)
	bPoint, pPoint := calculatePoints(bCard), calculatePoints(pCard)

	// 判断庄家和闲家赢的情况
	switch combinationType {
	case BankerWin:
		return !bPair && bPoint > pPoint, bCard, pCard
	case PlayerWin:
		return !pPair && bPoint < pPoint, bCard, pCard
	case Equal:
		return bPoint == pPoint, bCard, pCard
	case PairBankWin:
		return bPair && bPoint > pPoint, bCard, pCard
	case PairPlayerWin:
		return pPair && bPoint < pPoint, bCard, pCard
	}
	return false, bCard, pCard
}

// 计算庄家和闲家的牌型（决定是否需要补牌）
func calcDrawCard(cards []Card) ([]Card, []Card) {
	banker := []Card{cards[0], cards[1]} // 01 4
	player := []Card{cards[2], cards[3]} // 23 5

	b, p := calculatePoints(banker), calculatePoints(player)

	// 7点以上的牌不补
	if b > 7 || p > 7 {
		return banker, player
	}

	// 闲家大于5点，庄家小于6点，庄家需要补牌
	if p > 5 && b < 6 {
		return append(banker, cards[4]), player
	}

	// 闲家补牌
	player = append(player, cards[5])
	p = calculatePoints(player)

	if shouldBankerDraw(b, p) {
		banker = append(banker, cards[4])
	}

	return banker, player
}

// 判断庄家是否需要补牌的规则
func shouldBankerDraw(b, p int) bool {
	switch b {
	case 0, 1, 2:
		return true
	case 3:
		return p != 8
	case 4:
		return p != 0 && p != 1 && p != 8 && p != 9
	case 5:
		return !(p == 4 || p == 5 || p == 6 || p == 7)
	case 6:
		return p == 6 || p == 7
	}
	return false
}

// 计算牌的点数，返回点数 0-9
func calculatePoints(hand []Card) int {
	point := 0
	for _, v := range hand {
		point += v.Num
	}
	return point % 10
}

// 判断是否为对子
func isPair(cards []Card) bool {
	return cards[0].Num == cards[1].Num
}

// 将整数转换为 Card 类型
func intToCards(deck []int) []Card {
	cards := make([]Card, len(deck))
	for i, val := range deck {
		value := val & 0x0f       // 获取牌值的低四位
		suit := (val & 0xf0) >> 4 // 获取花色的高四位
		num := value
		if value >= 10 {
			num = 0
		}
		cards[i] = Card{Value: value, Suit: suit, Num: num}
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
