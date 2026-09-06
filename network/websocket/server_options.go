package websocket

import (
	"crypto/tls"
	"net"
	"net/url"
	"time"

	"yola/internal/tlsconfig"

	"github.com/go-kratos/kratos/v3/encoding"
	"github.com/go-kratos/kratos/v3/middleware"
)

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

// Codec configures protocol frame encoding. Peers must use the same codec.
// The package-owned protobuf default is not replaced by global codec registration.
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
