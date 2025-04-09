package main

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

func main() {
	// 创建 Redis 客户端
	rdb := redis.NewClient(&redis.Options{
		Addr: "192.168.56.131:6379", // Redis 地址
	})

	// 上下文
	ctx := context.Background()

	// 调用 INCRBYAndExpireInOneDay 函数
	key := "counter"
	increment := int64(10) // 增量

	newValue, err := INCRBYAndExpireInOneDay(ctx, rdb, key, increment)
	if err != nil {
		fmt.Println("Error:", err)
	} else {
		fmt.Printf("Key '%s' new value after increment: %d\n", key, newValue)
	}
}

// INCRBYAndExpireInOneDay 优化版本：只在第一次时设置过期时间
func INCRBYAndExpireInOneDay(ctx context.Context, client *redis.Client, key string, increment int64) (int64, error) {
	// 检查 key 是否存在，且没有过期时间
	ttl, err := client.TTL(ctx, key).Result()
	if err != nil {
		return 0, fmt.Errorf("failed to check TTL for key %s: %v", key, err)
	}

	// 如果没有过期时间（即 key 永久有效），则不需要每次设置过期时间
	if ttl == -1 {
		// 如果 key 已经有过期时间，则不再设置过期时间
		// 执行 INCRBY 操作
		newValue, err := client.IncrBy(ctx, key, increment).Result()
		if err != nil {
			return 0, fmt.Errorf("failed to increment key %s: %v", key, err)
		}
		return newValue, nil
	}

	// 创建管道
	pipe := client.Pipeline()

	// 执行 INCRBY 操作
	incrCmd := pipe.IncrBy(ctx, key, increment)

	// 设置过期时间为24小时（仅在第一次创建时设置）
	// 设置过期时间
	pipe.Expire(ctx, key, 24*time.Hour)

	// 执行所有命令
	_, err = pipe.Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to execute pipeline: %v", err)
	}

	// 获取 INCRBY 命令的结果
	return incrCmd.Val(), nil
}
