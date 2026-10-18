package node

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/url"
	"reflect"
	"time"

	"yola/internal/grpcendpoint"
	"yola/internal/tlsconfig"
	"yola/locate"

	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/registry"
	"github.com/go-kratos/kratos/v3/transport/grpc"
)

// Store 提供 Node 的分布式绑定、epoch 管理和健康检查。
type Store interface {
	locate.Pinger
	locate.NodeLocator
}

// GateLookup 查询玩家当前的 Gateway 连接。
type GateLookup interface {
	LocateGate(ctx context.Context, serviceName, uid string) (locate.GateLease, error)
}

// Option 配置 Node 服务。
type Option func(*options) error

type options struct {
	grpcOptions       []grpc.ServerOption
	clientTLS         *tls.Config
	pushTimeout       time.Duration
	cleanupTimeout    time.Duration
	shutdownTimeout   time.Duration
	network           string
	address           string
	listener          net.Listener
	advertiseHost     string
	endpoint          *url.URL
	serverTLS         bool
	middlewares       []middleware.Middleware
	clientMiddlewares []middleware.Middleware
	registrar         registry.Registrar
	stateful          Store
	gateDirectory     GateLookup
	drain             DrainFunc
}

func resolveOptions(opts ...Option) (options, error) {
	o := options{pushTimeout: 3 * time.Second, cleanupTimeout: 3 * time.Second, shutdownTimeout: 10 * time.Second, network: "tcp"}
	for _, opt := range opts {
		if err := opt(&o); err != nil {
			return options{}, err
		}
	}
	endpoint, err := grpcendpoint.Resolve(o.endpoint, o.address, o.advertiseHost, o.serverTLS)
	if err != nil {
		return options{}, fmt.Errorf("node: %w", err)
	}
	if endpoint != nil {
		o.grpcOptions = append(o.grpcOptions, grpc.Endpoint(endpoint))
	}
	if o.registrar == nil {
		return options{}, errors.New("node: registration is required")
	}
	if o.address == "" {
		o.address = ":0"
	}
	return o, nil
}

// Registration 配置 Node 内部目录登记；Registrar 必须安全回收失败申请，并按自身租约幂等注销。
// 租约型 Registrar 应实现 Done() <-chan struct{}：成功登记后失租或注销时关闭，Node 据此停止服务。
func Registration(registrar registry.Registrar) Option {
	return func(o *options) error {
		if registrar == nil {
			return errors.New("node: registration is required")
		}
		o.registrar = registrar
		return nil
	}
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

// Stateful 启用 Node 绑定与 epoch；store 不能为 nil（含带类型 nil）。
// 未配置 Stateful 时保持无状态。
func Stateful(store Store) Option {
	return func(o *options) error {
		if isNil(store) {
			return errors.New("node: stateful store is required")
		}
		o.stateful = store
		return nil
	}
}

// GateDirectory 配置 PushToUID 使用的 Gateway 查询，与 Stateful 模式相互独立。
// directory 不能为 nil（含带类型 nil）。
func GateDirectory(directory GateLookup) Option {
	return func(o *options) error {
		if isNil(directory) {
			return errors.New("node: gate directory is required")
		}
		o.gateDirectory = directory
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

// CleanupTimeout 限制启动失败后的独立回滚；默认 3 秒。
func CleanupTimeout(timeout time.Duration) Option {
	return func(o *options) error {
		if timeout <= 0 {
			return errors.New("node: cleanup timeout must be positive")
		}
		o.cleanupTimeout = timeout
		return nil
	}
}

// ShutdownTimeout 限制正常 Stop 和运行期失权后的独立排空；默认 10 秒。
// Stop 的调用者 context 只限制等待，不改变该预算。
func ShutdownTimeout(timeout time.Duration) Option {
	return func(o *options) error {
		if timeout <= 0 {
			return errors.New("node: shutdown timeout must be positive")
		}
		o.shutdownTimeout = timeout
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

func isNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
