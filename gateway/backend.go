package gateway

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"yola/api/cluster/v1"
	"yola/internal/clusterroute"
	"yola/internal/tlsconfig"
	"yola/locate"

	"github.com/go-kratos/kratos/v3/registry"
	kgrpc "github.com/go-kratos/kratos/v3/transport/grpc"
	"golang.org/x/sync/singleflight"
	"google.golang.org/grpc"
)

const _backendServiceConfig = `{"loadBalancingConfig":[{"` + _backendBalancerName + `":{}}]}`

var errBackendsClosed = errors.New("gateway: backends are closed")

type backends struct {
	discovery      registry.Discovery
	locator        locate.NodeLocator
	tlsConfig      *tls.Config
	connectTimeout time.Duration
	ctx            context.Context
	cancel         context.CancelFunc
	loads          singleflight.Group

	mu        sync.RWMutex
	byService map[string]*backend
}

type backend struct {
	conn   *grpc.ClientConn
	client v1.NodeClient
	state  *backendState
}

// nodeRoute 将选定的归属凭据与精确 RPC 路由保持一致。
type nodeRoute struct {
	client  v1.NodeClient
	binding locate.GateBinding
	nodeID  string
	epoch   string
}

type backendSnapshot struct {
	available    bool
	sticky       bool
	hostByNodeID map[string]string
}

type backendState struct {
	snapshot atomic.Pointer[backendSnapshot]
}

func newBackends(discovery registry.Discovery, tlsConfig *tls.Config, connectTimeout time.Duration, locator locate.NodeLocator) *backends {
	tlsConfig = tlsconfig.Clone(tlsConfig)
	ctx, cancel := context.WithCancel(context.Background())
	return &backends{
		discovery:      discovery,
		locator:        locator,
		tlsConfig:      tlsConfig,
		connectTimeout: connectTimeout,
		ctx:            ctx,
		cancel:         cancel,
		byService:      make(map[string]*backend),
	}
}

// resolveForward 为已有粘性归属补充 epoch，断线通知不需要此凭据。
func (b *backends) resolveForward(ctx context.Context, binding locate.GateBinding) (nodeRoute, error) {
	route, err := b.resolveNode(ctx, binding)
	if err != nil || route.nodeID == "" {
		return route, err
	}
	epoch, err := b.locator.LocateNodeEpoch(ctx, binding.ServiceName, route.nodeID)
	if err != nil {
		return nodeRoute{}, err
	}
	route.epoch = epoch
	return route, nil
}

func (b *backends) resolveNode(ctx context.Context, binding locate.GateBinding) (nodeRoute, error) {
	if err := ctx.Err(); err != nil {
		return nodeRoute{}, err
	}
	nodeBackend, err := b.get(ctx, binding.ServiceName)
	if err != nil {
		return nodeRoute{}, err
	}
	snapshot := nodeBackend.state.load()
	if !snapshot.available {
		return nodeRoute{}, fmt.Errorf("gateway: node service %q is unavailable", binding.ServiceName)
	}
	route := nodeRoute{client: nodeBackend.client, binding: binding}
	if !snapshot.sticky {
		return route, nil
	}
	nodeID, err := b.locator.LocateNode(ctx, binding.ServiceName, binding.UID)
	if errors.Is(err, locate.ErrNodeNotFound) {
		return route, nil
	}
	if err != nil {
		return nodeRoute{}, err
	}
	if !locate.ValidNodeLocation(binding.ServiceName, binding.UID, nodeID) {
		return nodeRoute{}, locate.ErrInvalidNodeBinding
	}
	if _, exists := snapshot.hostByNodeID[nodeID]; !exists {
		return nodeRoute{}, fmt.Errorf("gateway: node %q is unavailable", nodeID)
	}
	route.nodeID = nodeID
	return route, nil
}

func (r nodeRoute) forward(ctx context.Context, command int32, body []byte) (*v1.ForwardReply, error) {
	return r.client.Forward(withNodeID(ctx, r.nodeID), &v1.ForwardRequest{
		Route:     clusterroute.FromBinding(r.binding),
		Command:   command,
		Body:      body,
		NodeId:    r.nodeID,
		NodeEpoch: r.epoch,
	}, grpc.WaitForReady(r.nodeID != ""))
}

func (r nodeRoute) disconnect(ctx context.Context) error {
	_, err := r.client.Disconnect(withNodeID(ctx, r.nodeID), &v1.DisconnectRequest{
		Route: clusterroute.FromBinding(r.binding),
	}, grpc.WaitForReady(r.nodeID != ""))
	return err
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

func (b *backends) connectAndCache(serviceName string) (*backend, error) {
	cached, err := b.cached(serviceName)
	if cached != nil || err != nil {
		return cached, err
	}
	connectCtx, cancel := context.WithTimeout(b.ctx, b.connectTimeout)
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
		kgrpc.WithTimeout(0), // 请求 context 持有预算，关闭 Kratos 隐式的 2s 超时。
	}
	if b.tlsConfig != nil {
		opts = append(opts, kgrpc.WithTLSConfig(b.tlsConfig.Clone()))
	}
	dialOpts := []grpc.DialOption{
		grpc.WithDefaultServiceConfig(_backendServiceConfig),
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

func (b *backends) cached(serviceName string) (*backend, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.byService == nil {
		return nil, errBackendsClosed
	}
	// byService 只保存建连完成的非 nil backend；nil 表示尚未建连。
	return b.byService[serviceName], nil
}

func (s *backendState) load() backendSnapshot {
	snapshot := s.snapshot.Load()
	if snapshot == nil {
		return backendSnapshot{}
	}
	return *snapshot
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

func (s *backendState) store(snapshot backendSnapshot) {
	s.snapshot.Store(&snapshot)
}
