package main

import (
	"fmt"
	"math"
	"math/rand"
	"testing"
	"time"
)

//
//func TestCalculateLearningRate(t *testing.T) {
//	//decayRate := 0.1      // 衰减速率
//	maxIterations := 1000 //
//	for i := 0; i < maxIterations; i++ {
//		factor := CalculateLearningRate(0.06, i, maxIterations, false)
//		//rate := 0.06 * (1.0 - math.Pow(decayRate, float64(i)/float64(maxIterations/10)))
//		fmt.Printf("i=%d rate=%.6f\n", i, factor)
//	}
//	//return initialLearningRate * math.Pow(decayRate, float64(iteration)/float64(maxIterations))
//}

// 非线性倍数函数，使用幂函数调节分布
func getMultiplier(min, max float64) float64 {
	// 基于指数或者幂函数生成倍数，确保非线性分布
	randValue := rand.Float64()
	return min + (max-min)*math.Pow(randValue, 2) // 使用平方函数增加低倍数的概率
}

func TestCalculateLearningRate2(t *testing.T) {
	// 设置随机数种子
	rand.Seed(time.Now().UnixNano())

	// 定义倍数范围
	//minMul := 0.0
	//maxMul := 30.0

	// 定义区间概率
	prob1 := 0.8  // 0 - 10
	prob2 := 0.15 // 10 - 20
	prob3 := 0.05 // 20 - 30

	// 计算区间的总概率和比例
	totalProb := prob1 + prob2 + prob3
	p1 := prob1 / totalProb
	p2 := prob2 / totalProb
	//p3 := prob3 / totalProb

	// 随机数选择区间
	choice := rand.Float64()

	// 生成倍数
	var multiplier float64
	if choice <= p1 {
		// 区间 [0, 10] 的倍数
		multiplier = getMultiplier(0, 10)
	} else if choice <= p1+p2 {
		// 区间 [10, 20] 的倍数
		multiplier = getMultiplier(10, 20)
	} else {
		// 区间 [20, 30] 的倍数
		multiplier = getMultiplier(20, 30)
	}

	// 打印生成的倍数
	fmt.Printf("Generated multiplier: %.2f\n", multiplier)
}
