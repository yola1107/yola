// Package etcd 统一 Yola 的注册、发现与 etcd client 生命周期。
package etcd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-kratos/kratos/contrib/registry/etcd/v3"
	"github.com/go-kratos/kratos/v3/registry"
	"go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/concurrency"
)

// Registry 拥有一个 App 的 client 和注册租约，复用官方 Discovery。
// 每个 Registry 只尝试注册一次，租约丢失后不自动重建记录。
type Registry struct {
	registry.Discovery
	client    *clientv3.Client
	namespace string
	operation chan struct{}

	attempted bool
	key       string
	leaseID   clientv3.LeaseID
	cancel    context.CancelFunc
	session   *concurrency.Session
}

const (
	defaultEndpoint    = "127.0.0.1:2379"
	defaultPrefix      = "/etcd/prefix"
	defaultDialTimeout = 3 * time.Second
)

type config struct {
	endpoints   []string
	prefix      string
	dialTimeout time.Duration
}

type Option func(*config)

// New 创建进程专用 Registry；调用方负责 Close。
func New(opts ...Option) (*Registry, error) {
	c := &config{
		endpoints:   []string{defaultEndpoint},
		prefix:      defaultPrefix,
		dialTimeout: defaultDialTimeout,
	}
	for _, opt := range opts {
		opt(c)
	}
	if len(c.endpoints) == 0 {
		return nil, errors.New("etcd: endpoints are empty")
	}
	if c.prefix == "" || !strings.HasPrefix(c.prefix, "/") {
		return nil, errors.New("etcd: prefix must start with '/'")
	}
	if c.dialTimeout <= 0 {
		return nil, errors.New("etcd: dial timeout must be positive")
	}
	client, err := clientv3.New(clientv3.Config{
		Endpoints:   append([]string(nil), c.endpoints...),
		DialTimeout: c.dialTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("etcd: create client: %w", err)
	}
	return newRegistry(client, c.prefix), nil
}

func newRegistry(client *clientv3.Client, prefix string) *Registry {
	return &Registry{
		Discovery: etcd.New(client, etcd.Namespace(prefix)),
		client:    client, namespace: prefix, operation: make(chan struct{}, 1),
	}
}

// WithEndpoints 配置 etcd 地址，兼容逗号分隔的装配参数。
func WithEndpoints(endpoints ...string) Option {
	return func(config *config) {
		config.endpoints = config.endpoints[:0]
		for _, values := range endpoints {
			for _, endpoint := range strings.Split(values, ",") {
				if endpoint = strings.TrimSpace(endpoint); endpoint != "" {
					config.endpoints = append(config.endpoints, endpoint)
				}
			}
		}
	}
}

// WithPrefix 配置发现 key 的 namespace，保留调用方已有的前缀。
func WithPrefix(prefix string) Option {
	return func(config *config) { config.prefix = prefix }
}

// WithDialTimeout 配置 etcd client 的建连预算。
func WithDialTimeout(timeout time.Duration) Option {
	return func(config *config) { config.dialTimeout = timeout }
}

// Close 关闭 client 及其租约续期；未显式注销的记录等待 TTL 回收。
func (r *Registry) Close() error {
	if r == nil || r.client == nil {
		return nil
	}
	return r.client.Close()
}
