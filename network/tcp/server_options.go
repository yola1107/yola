package tcp

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/url"
	"time"

	"yola/internal/tlsconfig"

	"github.com/go-kratos/kratos/v3/encoding"
	"github.com/go-kratos/kratos/v3/middleware"
)

type serverConfig struct {
	network          string
	address          string
	advertiseHost    string
	tls              *tls.Config
	codec            encoding.Codec
	middlewares      []middleware.Middleware
	timeout          time.Duration
	handshakeTimeout time.Duration
	heartbeatTimeout time.Duration
	writeTimeout     time.Duration
	maxConnLimit     int32
	maxConnPerIP     int32
	requestQueueSize int
}

// ServerOption is TCP server option.
type ServerOption func(o *Server)

// Network with server network.
func Network(network string) ServerOption {
	return func(s *Server) { s.config.network = network }
}

// Address with server address.
func Address(addr string) ServerOption {
	return func(s *Server) { s.config.address = addr }
}

// AdvertiseHost configures the host published in the server endpoint.
func AdvertiseHost(advertiseHost string) ServerOption {
	return func(s *Server) { s.config.advertiseHost = advertiseHost }
}

// Endpoint with server address.
func Endpoint(endpoint *url.URL) ServerOption {
	return func(s *Server) {
		if endpoint == nil {
			s.endpoint = nil
			return
		}
		configured := *endpoint
		s.endpoint = &configured
	}
}

// Listener uses lis for the server.
func Listener(lis net.Listener) ServerOption {
	return func(s *Server) { s.lis = lis }
}

// TLSConfig enables TLS with c.
func TLSConfig(c *tls.Config) ServerOption {
	return func(s *Server) { s.config.tls = tlsconfig.Clone(c) }
}

// Codec 配置帧编码；两端须使用相同 codec。Marshal 不得修改输入，返回的 bytes 须保持不可变。
func Codec(codec encoding.Codec) ServerOption {
	return func(s *Server) { s.config.codec = codec }
}

// Middleware configures message middleware.
func Middleware(m ...middleware.Middleware) ServerOption {
	return func(s *Server) { s.config.middlewares = append(s.config.middlewares, m...) }
}

// Timeout configures the maximum duration of one message handler call.
func Timeout(timeout time.Duration) ServerOption {
	return func(s *Server) { s.config.timeout = timeout }
}

// RequestQueueSize 限制支持独立心跳的 handler 在认证后等待执行的业务帧数。
// 队列满会关闭过载连接；当前执行的请求不占等待容量。
func RequestQueueSize(size int) ServerOption {
	return func(s *Server) { s.config.requestQueueSize = size }
}

// MaxConnLimit configures the maximum concurrent connections.
func MaxConnLimit(limit int32) ServerOption {
	return func(s *Server) { s.config.maxConnLimit = limit }
}

// MaxConnPerIP configures the maximum concurrent connections from one peer IP.
func MaxConnPerIP(limit int32) ServerOption {
	return func(s *Server) { s.config.maxConnPerIP = limit }
}

// HandshakeTimeout configures how long a connection may wait for its first protocol heartbeat.
func HandshakeTimeout(timeout time.Duration) ServerOption {
	return func(s *Server) { s.config.handshakeTimeout = timeout }
}

// HeartbeatTimeout configures how long an active connection may remain silent.
func HeartbeatTimeout(timeout time.Duration) ServerOption {
	return func(s *Server) { s.config.heartbeatTimeout = timeout }
}

// WriteTimeout configures the maximum duration of one frame write.
func WriteTimeout(timeout time.Duration) ServerOption {
	return func(s *Server) { s.config.writeTimeout = timeout }
}

func (s *Server) validateConfig() error {
	if s.config.codec == nil {
		return errors.New("tcp: codec is required")
	}
	if s.config.timeout < 0 {
		return errors.New("tcp: handler timeout cannot be negative")
	}
	if s.config.requestQueueSize <= 0 {
		return errors.New("tcp: request queue size must be positive")
	}
	if s.config.maxConnLimit <= 0 {
		return errors.New("tcp: connection limit must be positive")
	}
	if s.config.maxConnPerIP <= 0 {
		return errors.New("tcp: per-IP connection limit must be positive")
	}
	if s.config.handshakeTimeout <= 0 {
		return errors.New("tcp: handshake timeout must be positive")
	}
	if s.config.heartbeatTimeout <= 0 {
		return errors.New("tcp: heartbeat timeout must be positive")
	}
	if s.config.writeTimeout <= 0 {
		return errors.New("tcp: write timeout must be positive")
	}
	if err := tlsconfig.ValidateServer(s.config.tls); err != nil {
		return fmt.Errorf("tcp: %w", err)
	}
	if s.endpoint == nil {
		return nil
	}
	scheme := s.config.endpointScheme()
	if s.endpoint.Scheme != scheme || s.endpoint.Host == "" {
		return fmt.Errorf("tcp: endpoint must use %s:// with a host", scheme)
	}
	return nil
}

func (c serverConfig) endpointScheme() string {
	if c.tls != nil {
		return "tcps"
	}
	return "tcp"
}
