package websocket

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
	path             string
	tls              *tls.Config
	codec            encoding.Codec
	middlewares      []middleware.Middleware
	timeout          time.Duration
	allowedOrigins   map[string]struct{}
	handshakeTimeout time.Duration
	maxHeaderBytes   int
	channel          *ChannelConfig
	maxConnLimit     int32
	maxConnPerIP     int32
	requestQueueSize int
}

// ServerOption configures a WebSocket server.
type ServerOption func(*Server)

// Network configures the listener network.
func Network(network string) ServerOption {
	return func(s *Server) { s.config.network = network }
}

// Address configures the listener address.
func Address(addr string) ServerOption {
	return func(s *Server) { s.config.address = addr }
}

// AdvertiseHost configures the host published in the server endpoint.
func AdvertiseHost(advertiseHost string) ServerOption {
	return func(s *Server) { s.config.advertiseHost = advertiseHost }
}

// Path configures the WebSocket route.
func Path(path string) ServerOption {
	return func(s *Server) { s.config.path = path }
}

// AllowedOrigins allows WebSocket requests from the listed origins.
func AllowedOrigins(origins ...string) ServerOption {
	return func(s *Server) {
		s.config.allowedOrigins = make(map[string]struct{}, len(origins))
		for _, origin := range origins {
			s.config.allowedOrigins[origin] = struct{}{}
		}
	}
}

// Endpoint configures the registry endpoint.
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

// TLSConfig enables TLS.
func TLSConfig(c *tls.Config) ServerOption {
	return func(s *Server) { s.config.tls = tlsconfig.Clone(c) }
}

// Codec 配置帧编码；两端须使用相同 codec。Marshal 不得修改输入，返回的 bytes 须保持不可变。
// 全局 codec 注册不会替换包内默认 protobuf 编码。
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

// MaxConnLimit configures the maximum concurrent channels.
func MaxConnLimit(limit int32) ServerOption {
	return func(s *Server) { s.config.maxConnLimit = limit }
}

// MaxConnPerIP configures the maximum concurrent connections from one peer IP.
func MaxConnPerIP(limit int32) ServerOption {
	return func(s *Server) { s.config.maxConnPerIP = limit }
}

// ServerChannelConfig configures server-side WebSocket channels.
func ServerChannelConfig(c *ChannelConfig) ServerOption {
	return func(s *Server) { s.config.channel = c }
}

// HandshakeTimeout configures the WebSocket and HTTP header handshake timeout.
func HandshakeTimeout(timeout time.Duration) ServerOption {
	return func(s *Server) { s.config.handshakeTimeout = timeout }
}

// MaxHeaderBytes configures the maximum WebSocket handshake header size.
func MaxHeaderBytes(size int) ServerOption {
	return func(s *Server) { s.config.maxHeaderBytes = size }
}

func (s *Server) validateConfig() error {
	if !validPath(s.config.path) {
		return errors.New("websocket: invalid path")
	}
	if s.config.codec == nil {
		return errors.New("websocket: codec is required")
	}
	if err := s.config.channel.validate(); err != nil {
		return err
	}
	if s.config.maxConnLimit <= 0 {
		return errors.New("websocket: connection limit must be positive")
	}
	if s.config.maxConnPerIP <= 0 {
		return errors.New("websocket: per-IP connection limit must be positive")
	}
	if s.config.handshakeTimeout <= 0 {
		return errors.New("websocket: handshake timeout must be positive")
	}
	if s.config.maxHeaderBytes <= 0 {
		return errors.New("websocket: max header bytes must be positive")
	}
	if s.config.timeout < 0 {
		return errors.New("websocket: handler timeout cannot be negative")
	}
	if s.config.requestQueueSize <= 0 {
		return errors.New("websocket: request queue size must be positive")
	}
	if err := tlsconfig.ValidateServer(s.config.tls); err != nil {
		return fmt.Errorf("websocket: %w", err)
	}
	if s.endpoint == nil {
		return nil
	}
	scheme := s.config.endpointScheme()
	if s.endpoint.Scheme != scheme || s.endpoint.Host == "" {
		return fmt.Errorf("websocket: endpoint must use %s:// with a host", scheme)
	}
	if s.endpoint.Path != s.config.path {
		return errors.New("websocket: endpoint path must match server path")
	}
	return nil
}

func (c serverConfig) endpointScheme() string {
	if c.tls != nil {
		return "wss"
	}
	return "ws"
}

func validPath(path string) bool {
	parsed, err := url.ParseRequestURI(path)
	return err == nil && parsed.Path == path
}
