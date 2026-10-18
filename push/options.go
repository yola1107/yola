package push

import (
	"crypto/tls"
	"errors"
	"fmt"
	"time"

	"yola/internal/tlsconfig"

	"github.com/go-kratos/kratos/v3/middleware"
)

const _defaultTimeout = 3 * time.Second

// Option 配置 Sender。
type Option func(*options) error

type options struct {
	tlsConfig   *tls.Config
	middlewares []middleware.Middleware
	timeout     time.Duration
}

func resolveOptions(opts ...Option) (options, error) {
	configured := options{timeout: _defaultTimeout}
	for _, opt := range opts {
		if opt == nil {
			return options{}, errors.New("push: option is required")
		}
		if err := opt(&configured); err != nil {
			return options{}, err
		}
	}
	return configured, nil
}

// ClientTLS 配置到 Gateway 的 gRPC TLS，并验证对端证书。
func ClientTLS(config *tls.Config) Option {
	return func(o *options) error {
		if config == nil {
			return errors.New("push: gRPC TLS config is required")
		}
		if err := tlsconfig.ValidateClient(config); err != nil {
			return fmt.Errorf("push: gRPC %w", err)
		}
		o.tlsConfig = tlsconfig.Clone(config)
		return nil
	}
}

// ClientMiddleware 配置到 Gateway 的 Kratos RPC middleware。
func ClientMiddleware(m ...middleware.Middleware) Option {
	return func(o *options) error {
		for _, current := range m {
			if current == nil {
				return errors.New("push: client middleware is required")
			}
		}
		o.middlewares = append(o.middlewares, m...)
		return nil
	}
}

// Timeout 限制一次 Gate 查询和 Gateway RPC 的总时长，默认三秒。
func Timeout(timeout time.Duration) Option {
	return func(o *options) error {
		if timeout <= 0 {
			return errors.New("push: timeout must be positive")
		}
		o.timeout = timeout
		return nil
	}
}
