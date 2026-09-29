package gateway

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"yola/api/cluster/v1"
	"yola/internal/gateclient"
	"yola/internal/listener"
	"yola/locate"
	"yola/network"

	"github.com/go-kratos/kratos/v3/middleware/recovery"
	"github.com/go-kratos/kratos/v3/transport"
	"github.com/go-kratos/kratos/v3/transport/grpc"
)

var (
	_ network.ConnectionHandler = (*Server)(nil)
	_ transport.Server          = (*Server)(nil)
	_ transport.Endpointer      = (*Server)(nil)
)

type lifecycleState uint8

const (
	_stNew lifecycleState = iota
	_stPreparing
	_stPrepared
	_stStarted
	_stStopping
)

// Server 持有 Gateway 连接、Node 路由和内部 gRPC transport。
type Server struct {
	grpcServer   *grpc.Server
	grpcListener *listener.Owner

	authenticator  Authenticator
	locator        locate.Locator
	rpcTimeout     time.Duration
	connectTimeout time.Duration
	leaseTimeout   time.Duration
	cleanupTimeout time.Duration
	authTimeout    time.Duration
	leaseTTL       time.Duration
	broadcaster    *broadcaster
	transports     []ClientTransport

	identity identity
	backends *backends
	sessions *sessionRegistry
	gateways *gateclient.Client

	lifecycle lifecycle
	admission admission
}

// lifecycle 协调进程启动和停止，不参与单连接热路径。
type lifecycle struct {
	mu              sync.Mutex
	state           lifecycleState
	preparationDone chan struct{}
	stopOnce        sync.Once
	stopErr         error
}

// admission 控制是否接纳新连接和认证。
type admission struct {
	mu        sync.Mutex
	accepting atomic.Bool
	wg        sync.WaitGroup
}

type identity struct {
	id       string
	endpoint string
}

// NewServer 创建 Gateway 服务及其内部 gRPC transport。
func NewServer(opts ...Option) (*Server, error) {
	o, err := resolveOptions(opts...)
	if err != nil {
		return nil, err
	}
	sessions := &sessionRegistry{byConnID: make(map[string]*session)}
	server := &Server{
		authenticator:  o.auth,
		locator:        o.locator,
		rpcTimeout:     o.rpcTimeout,
		connectTimeout: o.connectTimeout,
		leaseTimeout:   o.leaseTimeout,
		cleanupTimeout: o.cleanupTimeout,
		authTimeout:    o.authTimeout,
		leaseTTL:       o.leaseTTL,
		transports:     o.transports,
		backends:       newBackends(o.discovery, o.clientTLS, o.connectTimeout),
		sessions:       sessions,
		gateways:       gateclient.New(o.clientTLS),
	}
	server.broadcaster = newBroadcaster(sessions, o.broadcastWorkers, o.broadcastQueueCapacity)
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
	v1.RegisterGatewayServer(server.grpcServer, &pushService{server: server})
	for index, clientTransport := range server.transports {
		if err := clientTransport.SetHandler(server); err != nil {
			return nil, fmt.Errorf("gateway: configure client transport %d: %w", index, err)
		}
	}
	return server, nil
}
