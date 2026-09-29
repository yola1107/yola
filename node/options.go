package node

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/url"
	"time"

	"yola/internal/grpcendpoint"
	"yola/internal/tlsconfig"
	"yola/locate"

	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/transport/grpc"
)

// Option 配置 Node 服务。
type Option func(*options) error

type options struct {
	grpcOptions       []grpc.ServerOption
	clientTLS         *tls.Config
	pushTimeout       time.Duration
	cleanupTimeout    time.Duration
	network           string
	address           string
	listener          net.Listener
	advertiseHost     string
	endpoint          *url.URL
	serverTLS         bool
	middlewares       []middleware.Middleware
	clientMiddlewares []middleware.Middleware
	locator           locate.Locator
	drain             DrainFunc
}

func resolveOptions(opts ...Option) (options, error) {
	o := options{pushTimeout: 3 * time.Second, cleanupTimeout: 3 * time.Second, network: "tcp"}
	for _, opt := range opts {
		if err := opt(&o); err != nil {
			return options{}, err
		}
	}
	if err := o.resolveEndpoint(); err != nil {
		return options{}, err
	}
	if o.address == "" {
		o.address = ":0"
	}
	return o, nil
}

func (o *options) resolveEndpoint() error {
	endpoint, err := grpcendpoint.Resolve(o.endpoint, o.address, o.advertiseHost, o.serverTLS)
	if err != nil {
		return fmt.Errorf("node: %w", err)
	}
	if endpoint == nil {
		return nil
	}
	o.grpcOptions = append(o.grpcOptions, grpc.Endpoint(endpoint))
	return nil
}

// Network 配置 Node 监听网络。
func Network(network string) Option {
	return func(o *options) error {
		if network == "" {
			return errors.New("node: network is required")
		}
		o.network = network
		return nil
	}
}

// Address 配置 Node 监听地址。
func Address(address string) Option {
	return func(o *options) error {
		if address == "" {
			return errors.New("node: address is required")
		}
		o.address = address
		return nil
	}
}

// AdvertiseHost 指定对外可达的 host，保留 Address 的监听端口。
func AdvertiseHost(host string) Option {
	return func(o *options) error {
		if host == "" {
			return errors.New("node: advertise host is required")
		}
		o.advertiseHost = host
		return nil
	}
}

// Endpoint 配置通过服务发现发布的 Node endpoint。
func Endpoint(endpoint *url.URL) Option {
	return func(o *options) error {
		if endpoint == nil {
			return errors.New("node: endpoint is required")
		}
		configured := *endpoint
		o.endpoint = &configured
		return nil
	}
}

// HandlerTimeout 配置内部请求 handler 超时。
func HandlerTimeout(timeout time.Duration) Option {
	return func(o *options) error {
		if timeout < 0 {
			return errors.New("node: handler timeout cannot be negative")
		}
		o.grpcOptions = append(o.grpcOptions, grpc.Timeout(timeout))
		return nil
	}
}

// Listener 配置已有的 Node listener。
func Listener(listener net.Listener) Option {
	return func(o *options) error {
		if listener == nil {
			return errors.New("node: listener is required")
		}
		o.listener = listener
		return nil
	}
}

// ServerTLS 配置内部 Node 服务的 TLS。
func ServerTLS(config *tls.Config) Option {
	return func(o *options) error {
		if config == nil {
			return errors.New("node: TLS config is required")
		}
		if err := tlsconfig.ValidateServer(config); err != nil {
			return fmt.Errorf("node: %w", err)
		}
		o.grpcOptions = append(o.grpcOptions, grpc.TLSConfig(tlsconfig.Clone(config)))
		o.serverTLS = true
		return nil
	}
}

// Middleware 配置 protobuf command handler 的 middleware。
func Middleware(m ...middleware.Middleware) Option {
	return func(o *options) error {
		o.middlewares = append(o.middlewares, m...)
		return nil
	}
}

// ClientMiddleware 配置 Node 到 Gateway 的 Kratos RPC middleware；调用方仍拥有 Push deadline。
func ClientMiddleware(m ...middleware.Middleware) Option {
	return func(o *options) error {
		o.clientMiddlewares = append(o.clientMiddlewares, m...)
		return nil
	}
}

// Locator 同时配置 Node 和 Gate 定位存储。
// 两类查询必须成对配置：sticky 路由需要 Node binding，
// PushToUID 需要 Gate binding；仅配置一半会将错误推迟到请求期间。
func Locator(locator locate.Locator) Option {
	return func(o *options) error {
		if locator == nil {
			return errors.New("node: locator is required")
		}
		o.locator = locator
		return nil
	}
}

// ClientTLS 为 Node 到 Gateway 的 gRPC 调用配置验证对端的 TLS。
func ClientTLS(config *tls.Config) Option {
	return func(o *options) error {
		if config == nil {
			return errors.New("node: gRPC TLS config is required")
		}
		if err := tlsconfig.ValidateClient(config); err != nil {
			return fmt.Errorf("node: gRPC %w", err)
		}
		o.clientTLS = tlsconfig.Clone(config)
		return nil
	}
}

// PushTimeout 配置 Node 到 Gateway 的推送超时。
func PushTimeout(timeout time.Duration) Option {
	return func(o *options) error {
		if timeout <= 0 {
			return errors.New("node: push timeout must be positive")
		}
		o.pushTimeout = timeout
		return nil
	}
}

// CleanupTimeout 限制准备和启动失败后的独立回滚，不改变正常 Stop 的调用方预算。
func CleanupTimeout(timeout time.Duration) Option {
	return func(o *options) error {
		if timeout <= 0 {
			return errors.New("node: cleanup timeout must be positive")
		}
		o.cleanupTimeout = timeout
		return nil
	}
}

// Drain 注册业务停止屏障，在已接纳请求完成后调用。
// 返回错误会阻止 Stop 主动释放 Node epoch。
func Drain(fn DrainFunc) Option {
	return func(o *options) error {
		o.drain = fn
		return nil
	}
}
