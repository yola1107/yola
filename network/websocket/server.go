package websocket

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sync"

	"yola/internal/contextwait"
	"yola/network"
	"yola/network/internal/host"

	"github.com/go-kratos/kratos/v3/transport"
	"github.com/gorilla/websocket"
)

var (
	_ transport.Server     = (*Server)(nil)
	_ transport.Endpointer = (*Server)(nil)
)

// Server 是 Gorilla WebSocket transport server。
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

// NewServer 创建 WebSocket transport server。
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
			requestQueueSize: network.DefaultRequestQueueSize,
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
		mux.HandleFunc(s.config.path, s.handleConnections)
	}
	s.httpServer.Handler = mux
	s.httpServer.ReadHeaderTimeout = s.config.handshakeTimeout
	s.httpServer.MaxHeaderBytes = s.config.maxHeaderBytes
	return s
}

// SetHandler 在 Start 前注入连接处理器。
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

// BeforeStart 在服务注册前校验配置并绑定 listener。
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

// Endpoint 返回服务 endpoint。
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

// Start 启动 WebSocket 服务。
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

// Stop 停止 HTTP 服务、关闭 WebSocket 连接，并在 ctx 预算内等待连接任务退出。
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
