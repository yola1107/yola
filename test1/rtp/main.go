package main

import (
	"fmt"
	"math"
	"math/rand"
	"time"
)

// 目标RTP
const targetRTP = 0.96

type tagResult struct {
	index int
	rtp   float64
	score float64
	delta float64
}

func (r *tagResult) desc() string {
	return fmt.Sprintf("{index:%d rtp:%.3f score:%.3f delta:%.5f}",
		r.index, r.rtp, r.score, r.delta)
}

func main() {
	count := 1
	for i := 0; i < count; i++ {
		main2()
	}
}

func main2() {
	// 初始参数
	const bet = 100.0
	const maxIterations = 1000
	const gas = 1.0
	const initialFactor = 0.1 // 初始波动范围（容忍度）
	const decayRate = 0.999   // 每次迭代后的衰减率

	// 模拟赢分的基础值
	var totalWin = 10648.0 //39131.0
	var totalBet = 12222.0
	var currBet = bet

	rand.Seed(time.Now().UnixNano())

	// 模拟每次洗牌1000次的结果
	scoreMap := make(map[int]float64)
	for i := 0; i < maxIterations; i++ {
		winScore := rand.Float64() * 1000
		scoreMap[i] = winScore
	}

	// 最优结果初始化
	best := tagResult{
		delta: math.MaxFloat64,
	}

	// 初始容忍度
	factor := initialFactor

	// 平滑收敛的目标：通过逐步减小波动范围来平滑逼近目标RTP
	for i := 0; i < maxIterations; i++ {
		// 获取当前的赢分（模拟每次洗牌的结果）
		winScore := scoreMap[i]

		// 计算当前RTP
		currentRTP := calcRTP(totalBet, totalWin, currBet, winScore, gas)

		// 计算目标函数值（当前RTP与目标RTP之间的差距）
		delta := math.Abs(currentRTP - targetRTP)

		// 如果当前的delta比上次的小，保存当前结果
		if delta < best.delta {
			best = tagResult{
				index: i,
				score: winScore,
				delta: delta,
				rtp:   currentRTP,
			}
		}

		// 控制容忍度：逐步减小容忍度（factor）以精细化收敛
		factor *= decayRate // 随着迭代次数减小容忍度

		// 如果delta小于当前容忍度（factor），认为已经收敛
		if delta < factor {
			fmt.Printf("收敛，提前退出：i=%d 当前RTP = %.6f，目标RTP = %.6f\n", i, currentRTP, targetRTP)
			break
		}

		// 每100次输出一次调试信息
		if i%100 == 0 {
			fmt.Printf("迭代次数: %d, 当前RTP: %.4f, delta: %.6f, winScore: %.2f\n",
				i, currentRTP, delta, winScore)
		}
	}

	// 最终结果
	currRtp := calcRTP(totalBet, totalWin, currBet, best.score, gas)
	fmt.Printf("%.3f [%.3f %.3f] 目标RTP=%.3f factor=%.3f best=%+v\n",
		currRtp, targetRTP-factor, targetRTP+factor, targetRTP, factor, best.desc())
}

// 计算当前RTP
func calcRTP(totalBet, totalWin, bet, winScore, gas float64) float64 {
	return (totalWin + winScore) / ((totalBet + bet) * gas)
}

// 随机生成一个指定范围内的浮动数值
func RandFloat(min, max float64) float64 {
	return min + (max-min)*rand.Float64()
}

// CalculateLearningRate 计算动态步长（指数衰减）
func CalculateLearningRate(factor float64, iteration, maxIterations int, reverse bool) float64 {
	decayRate := 0.1 // 衰减速率

	if reverse {
		// 目标RTP 0.95 当前RTP 1.02 则1.02 -> 0.95
		return factor * (1 - math.Pow(decayRate, float64(iteration)/float64(maxIterations)))
	}

	// 目标RTP 0.95 当前RTP 0.52 则0.52 -> 0.95
	return factor * (math.Pow(decayRate, float64(iteration)/float64(maxIterations)))
}
