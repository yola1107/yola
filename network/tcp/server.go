package tcp

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"sync"
	"time"

	"yola/internal/contextwait"
	"yola/internal/tlsconfig"
	"yola/network"
	"yola/network/internal/host"

	"github.com/go-kratos/kratos/v3/encoding"
	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/transport"
)

var (
	_ transport.Server     = (*Server)(nil)
	_ transport.Endpointer = (*Server)(nil)
)

const (
	defaultSocketBufferSize = 4096
	defaultSendQueueSize    = network.DefaultSendQueueSize
	defaultHeartbeatTimeout = 15 * time.Second
)

// Server is a TCP server wrapper.
type Server struct {
	config           serverConfig
	cancel           context.CancelFunc
	lis              net.Listener
	endpoint         *url.URL
	lifecycleMu      sync.Mutex
	stopped          bool
	serveDone        chan struct{}
	conns            map[net.Conn]string
	connectionsPerIP map[string]int32
	connWG           sync.WaitGroup
	handler          network.ConnectionHandler
}

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
}

// NewServer creates a TCP transport server.
func NewServer(opts ...ServerOption) *Server {
	s := &Server{
		config: serverConfig{
			network:          "tcp",
			address:          ":3101",
			codec:            defaultCodec(),
			maxConnLimit:     network.DefaultMaxConnLimit,
			maxConnPerIP:     network.DefaultMaxConnPerIP,
			timeout:          network.DefaultHandlerTimeout,
			handshakeTimeout: network.DefaultHandshakeTimeout,
			heartbeatTimeout: defaultHeartbeatTimeout,
			writeTimeout:     network.DefaultWriteTimeout,
		},
		serveDone:        make(chan struct{}),
		conns:            make(map[net.Conn]string),
		connectionsPerIP: make(map[string]int32),
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// SetHandler injects the connection handler before Start.
func (s *Server) SetHandler(handler network.ConnectionHandler) error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if handler == nil {
		return errors.New("tcp: connection handler is required")
	}
	if s.cancel != nil || s.stopped {
		return errors.New("tcp: server cannot set handler after start")
	}
	if s.handler != nil {
		return errors.New("tcp: connection handler is already set")
	}
	s.handler = handler
	return nil
}

// BeforeStart validates the server and binds its listener before service registration.
func (s *Server) BeforeStart(_ context.Context) error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.cancel != nil || s.stopped {
		return errors.New("tcp: server cannot be prepared")
	}
	return s.prepare()
}

func (s *Server) prepare() error {
	if s.handler == nil {
		return errors.New("tcp: connection handler is required")
	}
	if err := s.validateConfig(); err != nil {
		return err
	}
	return s.listenAndEndpoint()
}

func (s *Server) validateConfig() error {
	if s.config.codec == nil {
		return errors.New("tcp: codec is required")
	}
	if s.config.timeout < 0 {
		return errors.New("tcp: handler timeout cannot be negative")
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

// Start starts the TCP server
func (s *Server) Start(ctx context.Context) error {
	s.lifecycleMu.Lock()
	if s.cancel != nil || s.stopped {
		s.lifecycleMu.Unlock()
		return errors.New("tcp: server cannot be started")
	}
	if err := s.prepare(); err != nil {
		s.lifecycleMu.Unlock()
		return err
	}
	baseCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	lis := s.lis
	if s.config.tls != nil {
		lis = tls.NewListener(lis, s.config.tls)
	}
	s.lifecycleMu.Unlock()
	slog.Info("[tcp] server listening", "addr", lis.Addr().String())
	defer s.finishServing()
	if err := s.acceptTCP(baseCtx, lis); !errors.Is(err, net.ErrClosed) {
		return err
	}
	return nil
}

// Stop gracefully shuts down the server
func (s *Server) Stop(ctx context.Context) error {
	slog.Info("[tcp] server stopping")
	s.lifecycleMu.Lock()
	var lis net.Listener
	if !s.stopped {
		s.stopped = true
		if s.cancel != nil {
			s.cancel()
		}
		lis = s.lis
		s.lis = nil
	}
	started := s.cancel != nil
	s.lifecycleMu.Unlock()
	var closeErr error
	if lis != nil {
		closeErr = lis.Close()
	}
	s.closeConnections()
	if started {
		if err := contextwait.Done(ctx, s.serveDone); err != nil {
			return err
		}
	}
	if closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
		return closeErr
	}
	return nil
}

func (s *Server) finishServing() {
	s.lifecycleMu.Lock()
	s.stopped = true
	cancel := s.cancel
	lis := s.lis
	s.lis = nil
	s.lifecycleMu.Unlock()
	cancel()
	if lis != nil {
		_ = lis.Close()
	}
	s.closeConnections()
	s.connWG.Wait()
	close(s.serveDone)
}

func (s *Server) closeConnections() {
	s.lifecycleMu.Lock()
	connections := make([]net.Conn, 0, len(s.conns))
	for conn := range s.conns {
		connections = append(connections, conn)
	}
	s.lifecycleMu.Unlock()
	for _, conn := range connections {
		_ = conn.Close()
	}
}

// Endpoint returns the server endpoint
func (s *Server) Endpoint() (*url.URL, error) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.stopped {
		return nil, errors.New("tcp: server is stopped")
	}
	if err := s.validateConfig(); err != nil {
		return nil, err
	}
	if err := s.listenAndEndpoint(); err != nil {
		return nil, err
	}
	endpoint := *s.endpoint
	return &endpoint, nil
}

// listenAndEndpoint sets up the listener and endpoint
func (s *Server) listenAndEndpoint() error {
	created := false
	if s.lis == nil {
		lis, err := net.Listen(s.config.network, s.config.address)
		if err != nil {
			return err
		}
		s.lis = lis
		created = true
	}
	if s.endpoint == nil {
		addr, err := host.Extract(s.config.address, s.config.advertiseHost, s.lis)
		if err != nil {
			if created {
				_ = s.lis.Close()
				s.lis = nil
			}
			return err
		}
		s.endpoint = &url.URL{Scheme: s.config.endpointScheme(), Host: addr}
	}
	return nil
}
