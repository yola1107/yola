package main

import (
	"fmt"
	"testing"
)

func TestCalculateLearningRate(t *testing.T) {
	//decayRate := 0.1      // 衰减速率
	maxIterations := 1000 //
	for i := 0; i < maxIterations; i++ {
		factor := CalculateLearningRate(0.06, i, maxIterations, false)
		//rate := 0.06 * (1.0 - math.Pow(decayRate, float64(i)/float64(maxIterations/10)))
		fmt.Printf("i=%d rate=%.6f\n", i, factor)
	}
	//return initialLearningRate * math.Pow(decayRate, float64(iteration)/float64(maxIterations))
}
