package gateway

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	clusterv1 "yola/api/cluster/v1"
	"yola/internal/gateclient"
	"yola/internal/listener"
	"yola/locate"
	"yola/network"

	"github.com/go-kratos/kratos/v3/middleware/recovery"
	"github.com/go-kratos/kratos/v3/transport"
	kgrpc "github.com/go-kratos/kratos/v3/transport/grpc"
)

const shutdownWorkerCount = 64

var (
	errInvalidPush       = errors.New("invalid push")
	errInvalidKick       = errors.New("invalid kick")
	errConnectionMissing = errors.New("connection not found")
	errBindingChanged    = errors.New("gate binding changed")
	errConnectionBusy    = errors.New("connection queue is full")
	errConnectionClosed  = errors.New("connection is unavailable")
)

var (
	_ network.ConnectionHandler = (*Server)(nil)
	_ transport.Server          = (*Server)(nil)
	_ transport.Endpointer      = (*Server)(nil)
)

type lifecycleState uint8

const (
	stNew lifecycleState = iota
	stPreparing
	stPrepared
	stStarted
	stStopping
)

// Server owns Gateway connections, Node routing, and the internal gRPC transport.
type Server struct {
	grpcServer   *kgrpc.Server
	grpcListener *listener.Owner

	authenticator Authenticator
	locator       locate.Locator
	rpcTimeout    time.Duration
	authTimeout   time.Duration
	leaseTTL      time.Duration
	broadcaster   *broadcaster
	transports    []ClientTransport

	identity identity
	backends *backends
	sessions *sessionRegistry
	gateways *gateclient.Client

	lifecycle lifecycle
	admission admission
}

// lifecycle owns process start/stop coordination (not per-connection hot paths).
type lifecycle struct {
	mu              sync.Mutex
	state           lifecycleState
	preparationDone chan struct{}
	stopOnce        sync.Once
	stopErr         error
}

// admission owns whether new connections/auths are accepted.
type admission struct {
	mu        sync.Mutex
	accepting atomic.Bool
	wg        sync.WaitGroup
}

type identity struct {
	id       string
	endpoint string
}

// NewServer creates a Gateway server and its internal gRPC transport.
func NewServer(opts ...Option) (*Server, error) {
	o, err := resolveOptions(opts...)
	if err != nil {
		return nil, err
	}
	sessions := &sessionRegistry{byConnID: make(map[string]*session)}
	server := &Server{
		authenticator: o.auth,
		locator:       o.locator,
		rpcTimeout:    o.rpcTimeout,
		authTimeout:   o.authTimeout,
		leaseTTL:      o.leaseTTL,
		transports:    o.transports,
		backends:      newBackends(o.discovery, o.clientTLS, o.rpcTimeout),
		sessions:      sessions,
		gateways:      gateclient.New(o.clientTLS),
	}
	server.broadcaster = newBroadcaster(sessions, o.broadcastWorkers, o.broadcastQueueCapacity)
	server.grpcListener = listener.New(o.network, o.address, o.listener)
	grpcOptions := make([]kgrpc.ServerOption, 0, len(o.grpcOptions)+5)
	grpcOptions = append(grpcOptions,
		kgrpc.Network(o.network),
		kgrpc.Address(o.address),
		kgrpc.Listener(server.grpcListener),
		kgrpc.Timeout(network.DefaultHandlerTimeout),
		kgrpc.Middleware(recovery.Recovery()),
	)
	grpcOptions = append(grpcOptions, o.grpcOptions...)
	server.grpcServer = kgrpc.NewServer(grpcOptions...)
	clusterv1.RegisterGatewayServer(server.grpcServer, &pushService{server: server})
	for index, clientTransport := range server.transports {
		if err := clientTransport.SetHandler(server); err != nil {
			return nil, fmt.Errorf("gateway: configure client transport %d: %w", index, err)
		}
	}
	return server, nil
}
