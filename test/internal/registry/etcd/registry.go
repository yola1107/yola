package etcd

import (
	"errors"
	"fmt"
	"strings"
	"time"

	kratosetcd "github.com/go-kratos/kratos/contrib/registry/etcd/v3"
	"github.com/go-kratos/kratos/v3/registry"
	"go.etcd.io/etcd/client/v3"
)

type Registry struct {
	registry.Registrar
	registry.Discovery
	client *clientv3.Client
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
	provider := kratosetcd.New(client, kratosetcd.Namespace(c.prefix), kratosetcd.Context(client.Ctx()))
	return &Registry{Registrar: provider, Discovery: provider, client: client}, nil
}

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

func WithPrefix(prefix string) Option {
	return func(config *config) { config.prefix = prefix }
}

func WithDialTimeout(timeout time.Duration) Option {
	return func(config *config) { config.dialTimeout = timeout }
}

func (registry *Registry) Close() error {
	if registry == nil || registry.client == nil {
		return nil
	}
	return registry.client.Close()
}
