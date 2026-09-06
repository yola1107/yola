package gateway

import (
	"context"
	"crypto/tls"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"yola/api/cluster/v1"
	"yola/locate"

	"github.com/go-kratos/kratos/v3/registry"
	kgrpc "github.com/go-kratos/kratos/v3/transport/grpc"
	"golang.org/x/sync/singleflight"
	"google.golang.org/grpc"
)

const backendServiceConfig = `{"loadBalancingConfig":[{"` + backendBalancerName + `":{}}]}`

var errBackendsClosed = fmt.Errorf("gateway: backends are closed")

type backends struct {
	discovery  registry.Discovery
	tlsConfig  *tls.Config
	rpcTimeout time.Duration
	ctx        context.Context
	cancel     context.CancelFunc
	loads      singleflight.Group

	mu        sync.RWMutex
	byService map[string]*backend
}

type backend struct {
	conn   *grpc.ClientConn
	client v1.NodeClient
	state  *backendState
}

type backendSnapshot struct {
	available    bool
	sticky       bool
	hostByNodeID map[string]string
}

type backendState struct {
	snapshot atomic.Pointer[backendSnapshot]
}

func (s *backendState) store(snapshot backendSnapshot) {
	s.snapshot.Store(&snapshot)
}

func (s *backendState) mode() (sticky bool, initialized bool) {
	snapshot := s.snapshot.Load()
	if snapshot == nil {
		return false, false
	}
	return snapshot.sticky, true
}

func (s *backendState) markUnavailable() {
	snapshot := s.snapshot.Load()
	if snapshot != nil {
		s.store(backendSnapshot{sticky: snapshot.sticky})
	}
}

func (s *backendState) load() backendSnapshot {
	snapshot := s.snapshot.Load()
	if snapshot == nil {
		return backendSnapshot{}
	}
	return *snapshot
}

func newBackends(discovery registry.Discovery, tlsConfig *tls.Config, rpcTimeout time.Duration) *backends {
	if tlsConfig != nil {
		tlsConfig = tlsConfig.Clone()
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &backends{
		discovery:  discovery,
		tlsConfig:  tlsConfig,
		rpcTimeout: rpcTimeout,
		ctx:        ctx,
		cancel:     cancel,
		byService:  make(map[string]*backend),
	}
}

func (b *backends) get(ctx context.Context, serviceName string) (*backend, error) {
	if !locate.ValidServiceName(serviceName) {
		return nil, fmt.Errorf("gateway: invalid node service %q", serviceName)
	}
	cached, err := b.cached(serviceName)
	if cached != nil || err != nil {
		return cached, err
	}

	loaded := b.loads.DoChan(serviceName, func() (any, error) {
		return b.connectAndCache(serviceName)
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-b.ctx.Done():
		return nil, errBackendsClosed
	case result := <-loaded:
		if b.ctx.Err() != nil {
			return nil, errBackendsClosed
		}
		if result.Err != nil {
			return nil, result.Err
		}
		return result.Val.(*backend), nil
	}
}

func (b *backends) connectAndCache(serviceName string) (*backend, error) {
	cached, err := b.cached(serviceName)
	if cached != nil || err != nil {
		return cached, err
	}
	connectCtx, cancel := context.WithTimeout(b.ctx, b.rpcTimeout)
	defer cancel()
	connected, err := b.connectBackend(connectCtx, serviceName)
	if err != nil {
		return nil, err
	}

	b.mu.Lock()
	if b.byService == nil {
		b.mu.Unlock()
		_ = connected.conn.Close()
		return nil, errBackendsClosed
	}
	b.byService[serviceName] = connected
	b.mu.Unlock()
	return connected, nil
}

func (b *backends) cached(serviceName string) (*backend, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.byService == nil {
		return nil, errBackendsClosed
	}
	// byService only stores fully connected, non-nil backends; nil means not dialed yet.
	return b.byService[serviceName], nil
}

func (b *backends) connectBackend(ctx context.Context, serviceName string) (*backend, error) {
	instances, err := b.discovery.GetService(ctx, serviceName)
	if err != nil {
		return nil, fmt.Errorf("gateway: discover node service %q: %w", serviceName, err)
	}
	snapshot, _, err := resolveBackendInstances(serviceName, instances, b.tlsConfig != nil)
	if err != nil {
		return nil, err
	}
	if !snapshot.available {
		return nil, fmt.Errorf("gateway: node service %q is unavailable", serviceName)
	}
	state := &backendState{}
	state.store(snapshot)

	opts := []kgrpc.ClientOption{
		kgrpc.WithEndpoint("discovery:///" + serviceName),
		kgrpc.WithTimeout(0), // Disable Kratos' implicit 2s deadline; request context owns it.
	}
	if b.tlsConfig != nil {
		opts = append(opts, kgrpc.WithTLSConfig(b.tlsConfig.Clone()))
	}
	dialOpts := []grpc.DialOption{
		grpc.WithDefaultServiceConfig(backendServiceConfig),
		grpc.WithResolvers(&backendResolverBuilder{
			discovery: b.discovery,
			state:     state,
		}),
	}
	opts = append(opts, kgrpc.WithOptions(dialOpts...))
	conn, err := kgrpc.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("gateway: connect node service %q: %w", serviceName, err)
	}
	return &backend{
		conn:   conn,
		client: v1.NewNodeClient(conn),
		state:  state,
	}, nil
}

func (b *backends) close() {
	b.cancel()
	b.mu.Lock()
	byService := b.byService
	b.byService = nil
	b.mu.Unlock()
	for _, nodeBackend := range byService {
		_ = nodeBackend.conn.Close()
	}
}
