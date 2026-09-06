package websocket

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"yola/internal/contextwait"
	"yola/internal/tlsconfig"
	"yola/network"
	"yola/network/internal/host"

	"github.com/go-kratos/kratos/v3/encoding"
	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/transport"
	"github.com/gorilla/websocket"
)

var (
	_ transport.Server     = (*Server)(nil)
	_ transport.Endpointer = (*Server)(nil)
)

// Server is a Gorilla WebSocket transport server.
type Server struct {
	config      serverConfig
	httpServer  http.Server
	cancel      context.CancelFunc
	lifecycleMu sync.Mutex
	stopped     bool

	lis      net.Listener
	endpoint *url.URL
	upgrader *websocket.Upgrader

	channels         map[string]*Channel
	connectionsPerIP map[string]int32
	connCount        int32
	connWG           sync.WaitGroup
	connWaitOnce     sync.Once
	connDone         chan struct{}

	handler network.ConnectionHandler
}

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
}

// NewServer creates a WebSocket transport server.
func NewServer(opts ...ServerOption) *Server {
	s := &Server{
		config: serverConfig{
			network:          "tcp",
			address:          ":3102",
			path:             "/",
			channel:          defaultChannelConfig(),
			maxConnLimit:     DefaultMaxConnLimit,
			maxConnPerIP:     DefaultMaxConnPerIP,
			codec:            defaultCodec(),
			timeout:          network.DefaultHandlerTimeout,
			handshakeTimeout: DefaultHandshakeTimeout,
			maxHeaderBytes:   DefaultMaxHeaderBytes,
		},
		channels:         make(map[string]*Channel),
		connectionsPerIP: make(map[string]int32),
		connDone:         make(chan struct{}),
	}
	for _, opt := range opts {
		opt(s)
	}
	if s.config.channel != nil {
		channelConfig := *s.config.channel
		s.config.channel = &channelConfig
	}
	s.upgrader = &websocket.Upgrader{
		HandshakeTimeout: s.config.handshakeTimeout,
		ReadBufferSize:   DefaultReadBufSize,
		WriteBufferSize:  DefaultWriteBufSize,
		WriteBufferPool:  &sync.Pool{},
		CheckOrigin:      s.checkOrigin,
	}
	mux := http.NewServeMux()
	if validPath(s.config.path) {
		mux.Handle(s.config.path, s.handleConnections())
	}
	s.httpServer.Handler = mux
	s.httpServer.ReadHeaderTimeout = s.config.handshakeTimeout
	s.httpServer.MaxHeaderBytes = s.config.maxHeaderBytes
	return s
}

// SetHandler injects the connection handler before Start.
func (s *Server) SetHandler(handler network.ConnectionHandler) error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if handler == nil {
		return errors.New("websocket: connection handler is required")
	}
	if s.cancel != nil || s.stopped {
		return errors.New("websocket: server cannot set handler after start")
	}
	if s.handler != nil {
		return errors.New("websocket: connection handler is already set")
	}
	s.handler = handler
	return nil
}

// BeforeStart validates the server and binds its listener before service registration.
func (s *Server) BeforeStart(_ context.Context) error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.cancel != nil || s.stopped {
		return errors.New("websocket: server cannot be prepared")
	}
	return s.prepare()
}

func (s *Server) prepare() error {
	if s.handler == nil {
		return errors.New("websocket: connection handler is required")
	}
	if err := s.validateConfig(); err != nil {
		return err
	}
	return s.listenAndEndpoint()
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

// Endpoint returns the server endpoint.
func (s *Server) Endpoint() (*url.URL, error) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.stopped {
		return nil, errors.New("websocket: server is stopped")
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
		s.endpoint = &url.URL{Scheme: s.config.endpointScheme(), Host: addr, Path: s.config.path}
	}
	return nil
}

// Start starts the WebSocket server.
func (s *Server) Start(ctx context.Context) error {
	s.lifecycleMu.Lock()
	if s.cancel != nil || s.stopped {
		s.lifecycleMu.Unlock()
		return errors.New("websocket: server cannot be started")
	}
	if err := s.prepare(); err != nil {
		s.lifecycleMu.Unlock()
		return err
	}
	baseCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.httpServer.BaseContext = func(net.Listener) context.Context { return baseCtx }
	s.httpServer.TLSConfig = s.config.tls
	lis := s.lis
	s.lifecycleMu.Unlock()
	defer s.finishServing()
	slog.Info("[websocket] server listening", "addr", lis.Addr().String())
	var err error
	if s.config.tls != nil {
		err = s.httpServer.ServeTLS(lis, "", "")
	} else {
		err = s.httpServer.Serve(lis)
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) finishServing() {
	s.lifecycleMu.Lock()
	s.stopped = true
	s.lis = nil
	s.cancel()
	s.lifecycleMu.Unlock()
	_ = s.stopConnections(context.Background())
}

// Stop gracefully stops the WebSocket server.
func (s *Server) Stop(ctx context.Context) error {
	slog.Info("[websocket] server stopping")
	s.lifecycleMu.Lock()
	if !s.stopped {
		s.stopped = true
		if s.cancel != nil {
			s.cancel()
		}
	}
	lis := s.lis
	s.lis = nil
	s.lifecycleMu.Unlock()
	stopErr := s.httpServer.Shutdown(ctx)
	if stopErr != nil && ctx.Err() != nil {
		_ = s.httpServer.Close()
	}
	if lis != nil {
		_ = lis.Close()
	}
	if err := s.stopConnections(ctx); err != nil {
		return err
	}
	if errors.Is(stopErr, http.ErrServerClosed) {
		return nil
	}
	return stopErr
}

func (s *Server) stopConnections(ctx context.Context) error {
	s.closeChannels()
	s.connWaitOnce.Do(func() { go s.waitConnections() })
	return contextwait.Done(ctx, s.connDone)
}

func (s *Server) waitConnections() {
	s.connWG.Wait()
	close(s.connDone)
}

func (s *Server) closeChannels() {
	s.lifecycleMu.Lock()
	channels := s.channels
	s.channels = nil
	s.lifecycleMu.Unlock()
	for _, ch := range channels {
		_ = ch.closeWithReason("server shutdown")
	}
}

func (s *Server) reserveConnection(remoteIP string) bool {
	// The lifecycle lock orders the last Add before stopConnections begins Wait.
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.stopped || s.connCount >= s.config.maxConnLimit ||
		s.connectionsPerIP[remoteIP] >= s.config.maxConnPerIP {
		return false
	}
	s.connCount++
	s.connectionsPerIP[remoteIP]++
	s.connWG.Add(1)
	return true
}

func (s *Server) commitConnection(ctx context.Context, conn *websocket.Conn) *Channel {
	// Stop either snapshots this channel or wins first and rejects the commit.
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.stopped {
		return nil
	}
	ch := newChannel(ctx, conn, s.config.codec, *s.config.channel)
	s.channels[ch.ConnID()] = ch
	return ch
}

func (s *Server) releaseConnection(remoteIP string, ch *Channel) {
	s.lifecycleMu.Lock()
	if ch != nil {
		delete(s.channels, ch.ConnID())
	}
	if s.connectionsPerIP[remoteIP] == 1 {
		delete(s.connectionsPerIP, remoteIP)
	} else {
		s.connectionsPerIP[remoteIP]--
	}
	s.connCount--
	s.lifecycleMu.Unlock()
	s.connWG.Done()
}

func (s *Server) checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	if len(s.config.allowedOrigins) > 0 {
		_, ok := s.config.allowedOrigins[origin]
		return ok
	}
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || (u.Path != "" && u.Path != "/") {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

func (s *Server) rejectConnection(ctx context.Context, w http.ResponseWriter, remoteIP string) {
	w.WriteHeader(http.StatusServiceUnavailable)
	slog.WarnContext(ctx, "[websocket] connection rejected",
		"remote_ip", remoteIP,
		"connection_limit", s.config.maxConnLimit,
		"per_ip_limit", s.config.maxConnPerIP,
		"reason", "connection limit reached",
	)
}
