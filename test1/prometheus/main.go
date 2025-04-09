//package main
//
//import (
//	"math/rand"
//	"net/http"
//	"time"
//
//	"github.com/prometheus/client_golang/prometheus"
//	"github.com/prometheus/client_golang/prometheus/promauto"
//	"github.com/prometheus/client_golang/prometheus/promhttp"
//)
//
//func recordMetrics() {
//	go func() {
//		for {
//			x := RandFloat(0, 100)
//			if time.Now().Second()%2 == 0 {
//				onlinePlayers.Add(-x)
//			} else {
//				onlinePlayers.Add(x)
//			}
//			opsProcessed.Add(x)
//			time.Sleep(2 * time.Second)
//		}
//	}()
//}
//
//var (
//	opsProcessed = promauto.NewCounter(
//		prometheus.CounterOpts{
//			Name: "myapp_processed_ops_total",
//			Help: "The total number of processed events",
//		})
//
//	onlinePlayers = prometheus.NewGaugeVec(
//		prometheus.GaugeOpts{
//			Name: "game_online_players",
//			Help: "Current number of online players per game",
//		},
//		[]string{"game"}, // 以游戏名称作为标签
//	)
//)
//
//func main() {
//	recordMetrics()
//
//	http.Handle("/metrics", promhttp.Handler())
//	http.ListenAndServe(":2112", nil)
//}
//
//// 随机生成一个指定范围内的浮动数值
//func RandFloat(min, max float64) float64 {
//	return min + (max-min)*rand.Float64()
//}

/////-------------------------------------------------------------
//package main
//
//import (
//	"math/rand"
//	"net/http"
//	"sync"
//	"time"
//
//	"github.com/prometheus/client_golang/prometheus"
//	"github.com/prometheus/client_golang/prometheus/promhttp"
//)
//
//var (
//	onlinePlayers = prometheus.NewGaugeVec(
//		prometheus.GaugeOpts{
//			Name: "game_online_players",
//			Help: "Current number of online players per game",
//		},
//		[]string{"game"},
//	)
//	mutex sync.Mutex
//)
//
//func init() {
//	prometheus.MustRegister(onlinePlayers)
//}
//
//// 更新在线人数
//func updateOnlinePlayers(game string, count int) {
//	mutex.Lock()
//	defer mutex.Unlock()
//	onlinePlayers.WithLabelValues(game).Set(float64(count))
//}
//
//func simulateOnlinePlayers() {
//	games := []string{"game_a", "game_b", "game_c"}
//
//	for {
//		for _, game := range games {
//			count := rand.Intn(500) // 模拟 0 - 500 的在线人数
//			updateOnlinePlayers(game, count)
//		}
//		time.Sleep(5 * time.Second) // 每 5 秒更新一次
//	}
//}
//
//func main() {
//	// 启动 Prometheus 服务器
//	http.Handle("/metrics", promhttp.Handler())
//
//	// 启动模拟在线人数的协程
//	go simulateOnlinePlayers()
//
//	// 监听 8080 端口
//	http.ListenAndServe(":2112", nil)
//}

package main

import (
	"fmt"
	"math/rand"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// 定义 Prometheus 指标
var (
	httpRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total number of HTTP requests",
		},
		[]string{"method", "endpoint"},
	)

	requestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "Duration of HTTP requests",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "endpoint"},
	)

	onlinePlayers = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "game_online_players",
			Help: "Current number of online players per game",
		},
		[]string{"game"},
	)
)

func init() {
	// 注册 Prometheus 指标
	prometheus.MustRegister(httpRequestsTotal, requestDuration, onlinePlayers)
}

// 监控中间件：统计请求总数 & 请求耗时
func prometheusMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next() // 执行请求

		duration := time.Since(start).Seconds()
		method := c.Request.Method
		endpoint := c.FullPath()

		httpRequestsTotal.WithLabelValues(method, endpoint).Inc()
		requestDuration.WithLabelValues(method, endpoint).Observe(duration)
	}
}

// 模拟游戏在线人数
func simulateOnlinePlayers() {
	games := []string{"game_a", "game_b", "game_c"}

	x := rand.Int() % 10
	for i := 0; i < x; i++ {
		games = append(games, fmt.Sprintf("game_%d", i))
	}

	for {
		for _, game := range games {
			count := rand.Intn(100) // 模拟 0 - 500 人在线
			onlinePlayers.WithLabelValues(game).Set(float64(count))
		}
		time.Sleep(5 * time.Second) // 每 5 秒更新一次
	}
}

func main() {
	r := gin.Default()

	// 使用 Prometheus 监控中间件
	r.Use(prometheusMiddleware())

	// 业务接口
	r.GET("/game/:name", func(c *gin.Context) {
		game := c.Param("name")
		count := rand.Intn(500) // 假设从数据库获取在线人数
		onlinePlayers.WithLabelValues(game).Set(float64(count))

		c.JSON(http.StatusOK, gin.H{"game": game, "online_players": count})
	})

	// Prometheus 监控接口
	r.GET("/metrics", gin.WrapH(promhttp.Handler()))

	// 启动后台协程模拟在线人数
	go simulateOnlinePlayers()

	r.Run(":2112") // 启动 HTTP 服务器
}
