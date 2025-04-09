package main

import (
	"fmt"
	"math"
	"math/rand"
	"time"
)

type tagResult struct {
	index int
	rtp   float64
	score float64
	delta float64
}

// 主函数
func main() {
	// 参数设置
	const targetRTP = 0.95     // 目标RTP
	const factor = 0.03        // 波动因子
	const bet = 100.0          // 当前下注额
	const gas = 1.0            // 比例系数
	const maxIterations = 1000 // 最大迭代次数
	var totalBet = 10000.0     // 总下注额
	var totalWin = 9500.0      // 总赢金
	var currBet = bet          // 当前下注
	var learningRate = 0.1     // 初始学习率

	scoreMap := make(map[int]float64)

	// 设置随机数种子
	rand.Seed(time.Now().UnixNano())

	for i := 0; i < maxIterations; i++ {
		rand.Seed(time.Now().UnixNano())
		// 随机生成赢分
		winScore := RandFloat(100, 1000) //rand.Float64() * 1000
		scoreMap[i] = winScore
	}
	//best := tagResult{
	//	delta: math.MaxFloat64,
	//}

	// 遍历1000次，寻找最接近目标RTP的结果
	for i := 0; i < maxIterations; i++ {
		// 随机生成当前赢分（这里可以根据实际情况生成赢分的范围）
		winScore := scoreMap[i]

		// 计算当前RTP
		currentRTP := calcRTP(totalBet, totalWin, currBet, winScore, gas)

		// 计算当前RTP与目标RTP的差值
		delta := math.Abs(currentRTP - targetRTP)

		// 动态计算步长（学习率衰减）
		learningRate = calculateLearningRate(i, maxIterations, 0.1)

		// 根据差值调整当前下注
		adjustment := delta * learningRate
		if currentRTP < targetRTP {
			currBet += adjustment
		} else {
			currBet -= adjustment
		}

		// 确保RTP在允许的波动范围内
		currentRTP = clampRTP(currentRTP, targetRTP, factor)

		// 每100次输出调试信息
		fmt.Printf("迭代次数: %d, 当前RTP: %.4f, delta: %.4f, 当前下注: %.4f\n", i, currentRTP, delta, currBet)

		// 如果delta小于波动因子，则提前退出
		if delta < factor {
			fmt.Printf("收敛，提前退出：迭代次数: %d, 当前RTP = %.4f, 目标RTP = %.4f\n", i, currentRTP, targetRTP)
			break
		}
	}

	// 输出最终结果
	fmt.Printf("最终结果：当前RTP = %.4f，目标RTP = %.4f\n", calcRTP(totalBet, totalWin, currBet, 0, gas), targetRTP)
}

// 计算动态步长（指数衰减）
func calculateLearningRate(iteration, maxIterations int, initialLearningRate float64) float64 {
	decayRate := 0.1 // 衰减速率
	return initialLearningRate * math.Pow(decayRate, float64(iteration)/float64(maxIterations))
}

// 计算当前RTP
func calcRTP(totalBet, totalWin, bet, winScore, gas float64) float64 {
	return (totalWin + winScore) / ((totalBet + bet) * gas)
}

// 检查RTP是否超出允许的波动范围
func clampRTP(rtp, targetRTP, factor float64) float64 {
	lowerBound := targetRTP - factor
	upperBound := targetRTP + factor
	if rtp < lowerBound {
		return lowerBound
	} else if rtp > upperBound {
		return upperBound
	}
	return rtp
}

// 随机生成一个指定范围内的浮动数值
func RandFloat(min, max float64) float64 {
	return min + (max-min)*rand.Float64()
}
