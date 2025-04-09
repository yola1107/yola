package main

import (
	"fmt"
	"math/rand"
	"time"
)

// 定义倍数项结构体
type MultipleItem struct {
	Weight int
	Lower  float64
	High   float64
}

var _multiples = []MultipleItem{
	{Weight: 3000, Lower: 0, High: 0},
	{Weight: 770, Lower: 0, High: 1},
	{Weight: 1484, Lower: 1, High: 1.99},
	{Weight: 1294, Lower: 2, High: 2.99},
	{Weight: 1220, Lower: 3, High: 3.99},
	{Weight: 363, Lower: 4, High: 4.99},
	{Weight: 275, Lower: 5, High: 5.99},
	{Weight: 244, Lower: 6, High: 6.99},
	{Weight: 201, Lower: 7, High: 7.99},
	{Weight: 188, Lower: 8, High: 8.99},
	{Weight: 174, Lower: 9, High: 9.99},

	{Weight: 150, Lower: 10, High: 15},
	{Weight: 100, Lower: 15, High: 20},
	{Weight: 50, Lower: 20, High: 30},
	{Weight: 30, Lower: 30, High: 40},
	{Weight: 20, Lower: 40, High: 50},
	{Weight: 20, Lower: 50, High: 60},
}

// 计算RTP
func calcRTP(totalBet, totalWin float64) float64 {
	if totalBet == 0 {
		return 0
	}
	return totalWin / totalBet
}

// 根据倍数的权重调整概率
func adjustWeights() {
	var totalWeight int
	var lowWeight, highWeight int

	// 计算当前权重总和
	for _, item := range _multiples {
		totalWeight += item.Weight
		if item.High <= 10 {
			lowWeight += item.Weight
		} else {
			highWeight += item.Weight
		}
	}

	//计算低倍数和高倍数的比例
	lowWeightRatio := 0.8 * float64(totalWeight)
	highWeightRatio := 0.2 * float64(totalWeight)

	// 计算调整系数
	lowWeightFactor := lowWeightRatio / float64(lowWeight)
	highWeightFactor := highWeightRatio / float64(highWeight)

	// 根据调整系数更新权重
	for i, item := range _multiples {
		if item.High <= 10 {
			_multiples[i].Weight = int(float64(item.Weight) * lowWeightFactor)
		} else {
			_multiples[i].Weight = int(float64(item.Weight) * highWeightFactor)
		}
	}

	fmt.Printf("%+v\n", _multiples)
}

// 随机选择倍数
func chooseMultiplier() float64 {
	// 根据调整后的权重，选择倍数
	rand.Seed(time.Now().UnixNano())
	choice := rand.Float64()
	cumulativeProb := 0.0
	var selectedMultiplier float64

	// 计算权重总和
	totalWeight := 0
	for _, item := range _multiples {
		totalWeight += item.Weight
	}

	// 随机选择倍数
	for _, item := range _multiples {
		cumulativeProb += float64(item.Weight) / float64(totalWeight)
		if choice <= cumulativeProb {
			selectedMultiplier = rand.Float64()*(item.High-item.Lower) + item.Lower
			break
		}
	}

	return selectedMultiplier
}

// 模拟游戏过程
func simulateGame(numGames int, initialBet float64) {
	var totalBets float64
	var totalWins float64

	adjustWeights() // 调整权重

	// 模拟多局游戏
	for game := 0; game < numGames; game++ {
		bet := initialBet

		// 随机选择倍数
		multiplier := chooseMultiplier()

		// 计算赢分
		win := bet * multiplier

		totalWins += win
		totalBets += bet

		// 计算当前RTP
		RTPCurrent := calcRTP(totalBets, totalWins)

		// 打印每局结果
		fmt.Printf("Game %d: RTP=%.2f (%.2f %.2f) win=%.2f Bet=%.2f mul=%.2f \n", game+1, RTPCurrent, totalWins, totalBets, win, bet, multiplier)
	}

	// 打印最终RTP
	finalRTP := calcRTP(totalBets, totalWins)
	fmt.Printf("\nFinal RTP: %.2f\n", finalRTP)
}

func main() {
	// 初始化游戏设置
	initialBet := 1.0 // 每次下注 1 金币
	numGames := 1000  // 模拟 1000 局游戏

	// 运行模拟
	simulateGame(numGames, initialBet)
}
