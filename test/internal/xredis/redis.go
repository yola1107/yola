package xredis

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Option 配置函数类型
type Option func(*redis.UniversalOptions)

// NewClient 创建Redis客户端，可接受多个配置选项
func NewClient(opts ...Option) (redis.UniversalClient, error) {
	options := &redis.UniversalOptions{
		Addrs:           []string{"127.0.0.1:6379"},
		Password:        "",
		DB:              0,
		PoolSize:        10,
		MinIdleConns:    5,
		MaxIdleConns:    10,
		ConnMaxLifetime: 2 * time.Minute,
		ConnMaxIdleTime: 5 * time.Minute,
	}
	for _, opt := range opts {
		opt(options)
	}
	if err := validateOptions(options); err != nil {
		return nil, fmt.Errorf("configure Redis client: %w", err)
	}

	client := redis.NewUniversalClient(options)
	if err := client.Ping(context.Background()).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ping Redis: %w", err)
	}
	return client, nil
}

func validateOptions(options *redis.UniversalOptions) error {
	if len(options.Addrs) == 0 {
		return errors.New("redis addresses are empty")
	}
	for index, addr := range options.Addrs {
		addr = strings.TrimSpace(addr)
		if err := validateAddress(addr); err != nil {
			return fmt.Errorf("redis address %d: %w", index, err)
		}
		options.Addrs[index] = addr
	}
	if options.DB < 0 {
		return errors.New("redis database cannot be negative")
	}
	if options.PoolSize <= 0 {
		return errors.New("redis pool size must be positive")
	}
	if options.MinIdleConns < 0 {
		return errors.New("redis minimum idle connections cannot be negative")
	}
	if options.MaxIdleConns < 0 {
		return errors.New("redis maximum idle connections cannot be negative")
	}
	if options.ConnMaxLifetime <= 0 {
		return errors.New("redis connection maximum lifetime must be positive")
	}
	if options.ConnMaxIdleTime <= 0 {
		return errors.New("redis connection maximum idle time must be positive")
	}
	return nil
}

// WithAddress 设置Redis完整地址（支持集群或哨兵的多个地址）
func WithAddress(addrs ...string) Option {
	return func(o *redis.UniversalOptions) { o.Addrs = append([]string(nil), addrs...) }
}

// WithPassword 设置Redis密码
func WithPassword(pass string) Option {
	return func(o *redis.UniversalOptions) { o.Password = pass }
}

// WithDB 选择Redis数据库
func WithDB(db int) Option {
	return func(o *redis.UniversalOptions) { o.DB = db }
}

// WithPoolSize 设置连接池大小
func WithPoolSize(size int) Option {
	return func(o *redis.UniversalOptions) { o.PoolSize = size }
}

// WithMinIdleConn 设置最小空闲连接数
func WithMinIdleConn(n int) Option {
	return func(o *redis.UniversalOptions) { o.MinIdleConns = n }
}

// WithMaxIdleConn 设置最大空闲连接数
func WithMaxIdleConn(n int) Option {
	return func(o *redis.UniversalOptions) { o.MaxIdleConns = n }
}

// WithConnMaxLifetime 设置连接最大生存时间
func WithConnMaxLifetime(d time.Duration) Option {
	return func(o *redis.UniversalOptions) { o.ConnMaxLifetime = d }
}

// WithConnMaxIdleTime 设置连接最大空闲时间
func WithConnMaxIdleTime(d time.Duration) Option {
	return func(o *redis.UniversalOptions) { o.ConnMaxIdleTime = d }
}

func validateAddress(addr string) error {
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("must use host:port form: %w", err)
	}
	if strings.TrimSpace(host) == "" {
		return errors.New("host is empty")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	return nil
}

// 示例使用：
// client, err := xredis.NewClient(
//
//	xredis.WithAddress("127.0.0.1:6379", "127.0.0.1:6380"),
//	xredis.WithPassword("securepassword"),
//	xredis.WithDB(0),
//	xredis.WithPoolSize(20),
//	xredis.WithMinIdleConn(5),
//	xredis.WithMaxIdleConn(10),
//	xredis.WithConnMaxLifetime(10*time.Minute),
//	xredis.WithConnMaxIdleTime(30*time.Minute),
//
// )
