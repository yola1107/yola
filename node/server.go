package node

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"yola/api/cluster/v1"
	"yola/instance"
	"yola/internal/gateclient"
	"yola/internal/listener"
	"yola/locate"
	"yola/network"

	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/middleware/recovery"
	"github.com/go-kratos/kratos/v3/transport"
	"github.com/go-kratos/kratos/v3/transport/grpc"
)

var (
	_ transport.Server     = (*Server)(nil)
	_ transport.Endpointer = (*Server)(nil)
)

type lifecycleState uint8

const (
	stNew lifecycleState = iota
	stPreparing
	stPrepared
	stStarted
)

// Server is the Yola Node transport for a Kratos application.
type Server struct {
	grpcServer     *grpc.Server
	grpcListener   *listener.Owner
	pushTimeout    time.Duration
	cleanupTimeout time.Duration
	drain          DrainFunc

	handlers     map[int32]Handler
	middlewares  []middleware.Middleware
	onDisconnect DisconnectHandler
	gateways     *gateclient.Client
	locator      locate.Locator
	// identity 向请求路径发布不可变的服务身份快照。
	identity atomic.Pointer[nodeIdentity]

	// lifecycleMu 保护准备、启动和 lease 的交接；租约自身管理续租与清理。
	lifecycleMu     sync.Mutex
	fatalCtx        context.Context
	fatalCancel     context.CancelCauseFunc
	state           lifecycleState
	preparationDone chan struct{}
	lease           atomic.Pointer[epochLease]
	ready           chan struct{}
	readyOnce       sync.Once
	readyErr        error
	requests        requestAdmission
	// deliveries 覆盖绑定写入与 Push；业务 Drain 返回后才关闭准入。
	deliveries requestAdmission
	stopOnce   sync.Once
	stopErr    error
}

// DrainFunc 在入站请求排空后执行一次；返回前须停止业务的绑定与推送生产者。
type DrainFunc func(ctx context.Context) error

type nodeIdentity struct {
	serviceName string
	nodeID      string
	epoch       string
}

// NewServer creates a Node transport and its internal gRPC server.
func NewServer(opts ...Option) (*Server, error) {
	o, err := resolveOptions(opts...)
	if err != nil {
		return nil, err
	}
	fatalCtx, fatalCancel := context.WithCancelCause(context.Background())
	server := &Server{
		pushTimeout:    o.pushTimeout,
		cleanupTimeout: o.cleanupTimeout,
		drain:          o.drain,
		middlewares:    o.middlewares,
		locator:        o.locator,
		handlers:       make(map[int32]Handler),
		fatalCtx:       fatalCtx,
		fatalCancel:    fatalCancel,
		ready:          make(chan struct{}),
	}
	server.gateways = gateclient.New(o.clientTLS, o.clientMiddlewares...)
	server.grpcListener = listener.New(o.network, o.address, o.listener)
	grpcOptions := make([]grpc.ServerOption, 0, len(o.grpcOptions)+5)
	grpcOptions = append(grpcOptions,
		grpc.Network(o.network),
		grpc.Address(o.address),
		grpc.Listener(server.grpcListener),
		grpc.Timeout(network.DefaultHandlerTimeout),
		grpc.Middleware(recovery.Recovery()),
	)
	grpcOptions = append(grpcOptions, o.grpcOptions...)
	server.grpcServer = grpc.NewServer(grpcOptions...)
	v1.RegisterNodeServer(server.grpcServer, &forwardService{server: server})
	return server, nil
}

// Metadata returns sticky service metadata when a locator is configured.
func (s *Server) Metadata() map[string]string {
	if s == nil || s.locator == nil {
		return nil
	}
	return instance.StickyMetadata()
}

func (s *Server) currentIdentity() nodeIdentity {
	identity := s.identity.Load()
	if identity == nil {
		return nodeIdentity{}
	}
	return *identity
}
