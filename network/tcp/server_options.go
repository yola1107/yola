package tcp

import (
	"crypto/tls"
	"net"
	"net/url"
	"time"

	"yola/internal/tlsconfig"

	"github.com/go-kratos/kratos/v3/encoding"
	"github.com/go-kratos/kratos/v3/middleware"
)

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

// Codec configures protocol frame encoding. Peers must use the same codec.
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
