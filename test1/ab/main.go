//package main
//
//import (
//	"fmt"
//
//	"github.com/growthbook/growthbook-golang"
//	//"github.com/growthbook/growthbook-golang"
//)
//
//func main() {
//	// 创建 GrowthBook 实例
//	gb := growthbook.New(growthbook.Options{APIHost: "https://your-growthbook-server.com"})
//
//	// 假设 userID 3212486 进入实验
//	userID := "3212486"
//
//	// 定义 A/B 测试实验
//	experiment := growthbook.Experiment{
//		Key: "gift_amount", // 送金币实验
//		Variants: []interface{}{
//			1000,  // A 组：送 1000
//			10000, // B 组：送 10000
//		},
//	}
//
//	// 获取用户的实验分组
//	result := gb.Run(userID, experiment)
//	fmt.Printf("用户 %s 被分配到: %v 金币组\n", userID, result.Value)
//}

package main

import (
	"fmt"
	"math"

	"gonum.org/v1/gonum/stat/distuv"
)

// 计算卡方检验
func chiSquareTest(payingA, totalA, payingB, totalB int) (chi2 float64, pValue float64) {
	// 计算 2x2 卡方检验的观察值
	observed := []float64{float64(payingA), float64(totalA - payingA),
		float64(payingB), float64(totalB - payingB)}

	// 计算期望值（假设两组相同的情况下）
	totalUsers := totalA + totalB
	totalPaying := payingA + payingB
	totalNonPaying := totalUsers - totalPaying

	expected := []float64{
		float64(totalA) * float64(totalPaying) / float64(totalUsers),
		float64(totalA) * float64(totalNonPaying) / float64(totalUsers),
		float64(totalB) * float64(totalPaying) / float64(totalUsers),
		float64(totalB) * float64(totalNonPaying) / float64(totalUsers),
	}

	// 计算卡方统计量
	for i := 0; i < 4; i++ {
		if expected[i] > 0 {
			chi2 += math.Pow(observed[i]-expected[i], 2) / expected[i]
		}
	}

	// 计算 P 值（自由度 = 1）
	chiDist := distuv.ChiSquared{K: 1} // K=1 是自由度
	pValue = 1 - chiDist.CDF(chi2)     // 计算右侧 P 值

	// 输出结果
	fmt.Printf("Observed: %v\n", observed)
	fmt.Printf("Expected: %v\n", expected)
	fmt.Printf("Chi-Square: %.4f\n", chi2)
	fmt.Printf("P-value: %.4f\n", pValue)

	return chi2, pValue
}

func main() {
	// 实验数据
	payingA, totalA := 25, 500 // A组：付费25人，总用户500
	payingB, totalB := 40, 500 // B组：付费40人，总用户500

	// 计算卡方统计量 & P 值
	chi2, pValue := chiSquareTest(payingA, totalA, payingB, totalB)

	// 输出结果
	fmt.Printf("卡方统计量: %.4f\n", chi2)
	fmt.Printf("P 值: %.4f\n", pValue)

	// 解释结果
	if pValue < 0.05 {
		fmt.Println("结果显著，两组付费率存在差异！")
	} else {
		fmt.Println("无显著差异，需要更大数据量")
	}
}

//package main
//
//import (
//	"fmt"
//
//	"gonum.org/v1/gonum/stat"
//	"gonum.org/v1/gonum/stat/distuv"
//)
//
//func main() {
//	// 数据准备
//	payingA := 100 // 对照组付费用户数
//	totalA := 1000 // 对照组总用户数
//	payingB := 150 // 实验组付费用户数
//	totalB := 1000 // 实验组总用户数
//
//	// 构建观察值列联表
//	observed := []float64{
//		float64(payingA), float64(totalA - payingA),
//		float64(payingB), float64(totalB - payingB),
//	}
//
//	// 计算总付费率和总未付费率
//	totalPaid := float64(payingA + payingB)
//	totalUsers := float64(totalA + totalB)
//	paidRate := totalPaid / totalUsers
//	unpaidRate := 1 - paidRate
//
//	// 计算期望值
//	aExpectedPaid := paidRate * float64(totalA)
//	aExpectedUnpaid := unpaidRate * float64(totalA)
//	bExpectedPaid := paidRate * float64(totalB)
//	bExpectedUnpaid := unpaidRate * float64(totalB)
//
//	expected := []float64{
//		aExpectedPaid, aExpectedUnpaid,
//		bExpectedPaid, bExpectedUnpaid,
//	}
//
//	// 计算卡方统计量
//	chiSquare := stat.ChiSquare(observed, expected)
//
//	// 计算 p 值
//	df := 1 // 自由度 = (行数 - 1) * (列数 - 1) = (2-1)*(2-1) = 1
//	pValue := 1 - distuv.ChiSquared{K: float64(df)}.CDF(chiSquare)
//
//	// 输出结果
//	fmt.Printf("Observed: %v\n", observed)
//	fmt.Printf("Expected: %v\n", expected)
//	fmt.Printf("Chi-Square: %.4f\n", chiSquare)
//	fmt.Printf("P-value: %.4f\n", pValue)
//
//	// 判断显著性
//	alpha := 0.05 // 显著性水平
//	if pValue < alpha {
//		fmt.Println("付费率差异显著（拒绝原假设）")
//	} else {
//		fmt.Println("付费率差异不显著（无法拒绝原假设）")
//	}
//}

//package main
//
//import (
//	"log"
//
//	"github.com/segmentio/analytics-go"
//)
//
//func main() {
//	// 1. 创建 Segment 客户端（替换 "your_write_key" 为真实的 API Key）
//	client := analytics.New("your_write_key")
//	defer client.Close()
//
//	// 2. 记录用户事件（例如用户付费）
//	err := client.Enqueue(&analytics.Track{
//		UserId: "player_123", // 用户唯一 ID
//		Event:  "Purchase",   // 事件名称
//		Properties: map[string]interface{}{
//			"amount": 100.5,
//			"item":   "Gold Package",
//			"group":  "A", // A/B 测试分组
//		},
//	})
//
//	if err != nil {
//		log.Fatal(err)
//	}
//
//	log.Println("Event sent to Segment")
//}
