package main

import (
	"context"
	"fmt"
	"time"

	"github.com/antlabs/coalesce"
	"github.com/redis/go-redis/v9"
)

var rdb = redis.NewClient(&redis.Options{Addr: "localhost:6379"})

func main() {
	c := coalesce.New(100, 10*time.Millisecond) // 缓存 100 个请求，10ms 合并一次

	for i := 0; i < 1000; i++ {
		go func(i int) {
			c.Do("user:score", func() {
				ctx := context.Background()
				rdb.IncrBy(ctx, "user:total_score", 1) // 合并 Redis 更新
				fmt.Println("批量更新完成")
			})
		}(i)
	}

	time.Sleep(2 * time.Second)
}
