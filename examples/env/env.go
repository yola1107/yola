package env

import (
	"context"
	"fmt"
	"os"
	"time"

	eventnats "yola/event/nats"
	"yola/registry/etcd"

	"github.com/redis/go-redis/v9"
)

// 以下配置仅用于本仓库的本地示例测试，生产环境不得使用。
var (
	RedisAddr     = envOr("YOLA_REDIS_ADDR", "127.0.0.1:6379")
	RedisPassword = envOr("YOLA_REDIS_PASS", "")
	EtcdAddr      = envOr("YOLA_ETCD_ADDR", "127.0.0.1:2379")
	NATSURL       = envOr("YOLA_NATS_URL", "nats://127.0.0.1:4222")
	Host          = envOr("YOLA_ADVERTISE_HOST", "127.0.0.1")
)

func envOr(name, fallback string) string {
	if configured := os.Getenv(name); configured != "" {
		return configured
	}
	return fallback
}

// NewRedis creates a Redis client and verifies the configured credentials.
func NewRedis() (*redis.Client, error) {
	client := redis.NewClient(&redis.Options{Addr: RedisAddr, Password: RedisPassword})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ping Redis: %w", err)
	}
	return client, nil
}

// NewEventBus creates one ready-to-use, process-owned online event bus.
func NewEventBus() (*eventnats.Bus, error) {
	return eventnats.New(eventnats.WithURL(NATSURL))
}

// Registry 沿用示例类型名，实际资源由共享的 etcd Registry 管理。
type Registry = etcd.Registry

// NewRegistry creates the configured service registry.
func NewRegistry() (*Registry, error) {
	return etcd.New(etcd.WithEndpoints(EtcdAddr), etcd.WithPrefix("/microservices"))
}
