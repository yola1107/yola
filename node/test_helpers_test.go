package node

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yola/api/cluster/v1"
	"yola/locate"

	"github.com/go-kratos/kratos/v3"
	"github.com/go-kratos/kratos/v3/registry"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

var _ Store = (*memoryNodeStore)(nil)

func newTestServer(t testing.TB, opts ...Option) *Server {
	t.Helper()
	opts = append([]Option{Registration(&countingRegistrar{})}, opts...)
	server, err := NewServer(opts...)
	require.NoError(t, err)
	return server
}

func newDispatchTestServer(t testing.TB, opts ...Option) *Server {
	t.Helper()
	server := newTestServer(t, opts...)
	publishTestIdentity(server, nodeIdentity{serviceName: "game", nodeID: "node-a"})
	return server
}

func savedBindingSession(t *testing.T, locator Store, opts ...Option) (*Server, Session) {
	t.Helper()
	opts = append(opts, Stateful(locator))
	server := newTestServer(t, opts...)
	require.NoError(t, locator.RegisterNodeEpoch(context.Background(), "game", "node-a", "epoch-a", DefaultNodeEpochTTL))
	publishTestIdentity(server, nodeIdentity{serviceName: "game", nodeID: "node-a", epoch: "epoch-a"})
	var session Session
	server.RegisterRawHandler(1, func(ctx context.Context, _ []byte) ([]byte, error) {
		session, _ = FromContext(ctx)
		return nil, nil
	})
	_, err := server.forward(context.Background(), testBinding("player-a", "conn-a"), 1, nil)
	require.NoError(t, err)
	require.NotNil(t, session)
	return server, session
}

func startTestNode(ctx context.Context, t *testing.T, server *Server) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- server.Start(ctx) }()
	t.Cleanup(func() {
		_ = server.Stop(context.Background())
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("Node Start did not return after Stop")
		}
	})
	require.Eventually(t, func() bool {
		return server.requests.state.Load() == _admissionOpen
	}, time.Second, time.Millisecond)
}

func nodeClients(t *testing.T, record *registry.ServiceInstance) (v1.NodeClient, healthpb.HealthClient) {
	t.Helper()
	require.Len(t, record.Endpoints, 1)
	endpoint, err := url.Parse(record.Endpoints[0])
	require.NoError(t, err)
	conn, err := grpc.NewClient(endpoint.Host, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	return v1.NewNodeClient(conn), healthpb.NewHealthClient(conn)
}

func testNodeContext() context.Context {
	return kratos.NewContext(context.Background(), nodeTestAppInfo{id: "node-a", name: "game"})
}

func testBinding(uid, connID string) locate.GateBinding {
	return locate.GateBinding{
		ServiceName:  "game",
		UID:          uid,
		BindingToken: "binding-" + uid,
		GateID:       "gate-a",
		GateEndpoint: "grpc://127.0.0.1:9000",
		ConnID:       connID,
	}
}

func callSessionBinding(ctx context.Context, session Session, method string) error {
	switch method {
	case "BindNode":
		return session.BindNode(ctx)
	case "RenewNode":
		return session.RenewNode(ctx)
	default:
		return session.UnbindNode(ctx)
	}
}

func assertHandlerRegistrationFrozen(t *testing.T, server *Server) {
	t.Helper()
	require.PanicsWithValue(t, "node: handlers must be registered before Start", func() {
		server.RegisterRawHandler(1, func(context.Context, []byte) ([]byte, error) { return nil, nil })
	})
	require.PanicsWithValue(t, "node: handlers must be registered before Start", func() {
		server.OnDisconnect(func(context.Context, Session) error { return nil })
	})
}

func requireNodeDraining(t *testing.T, err error) {
	t.Helper()
	require.Equal(t, codes.Unavailable, status.Code(err))
	require.Contains(t, err.Error(), "node is draining")
}

func receiveNodeValue[T any](t testing.TB, values <-chan T) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Node test value")
		var zero T
		return zero
	}
}

func (s *Server) forward(ctx context.Context, binding locate.GateBinding, command int32, body []byte) ([]byte, error) {
	return s.forwardTo(ctx, stickyClaim{}, binding, command, body)
}

func publishTestIdentity(server *Server, identity nodeIdentity) {
	server.lifecycleMu.Lock()
	defer server.lifecycleMu.Unlock()
	server.identity.Store(&identity)
	server.requests.open()
	server.deliveries.open()
	if server.locator != nil && identity.epoch != "" {
		server.lease.Store(newEpochLease(server.locator, identity, time.Now().Add(DefaultNodeEpochTTL)))
	}
}

func testServerTLSConfig(t testing.TB) *tls.Config {
	t.Helper()
	serverTLS, _ := testTLSConfigs(t)
	return serverTLS
}

func testTLSConfigs(t testing.TB) (*tls.Config, *tls.Config) {
	t.Helper()
	certificateSource := httptest.NewTLSServer(http.NotFoundHandler())
	serverTLS := certificateSource.TLS.Clone()
	roots := x509.NewCertPool()
	roots.AddCert(certificateSource.Certificate())
	certificateSource.Close()
	return serverTLS, &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
}

// memoryNodeStore 模拟 Node 绑定和 epoch，不模拟 TTL 流逝。
type memoryNodeStore struct {
	mu        sync.Mutex
	bindings  map[string]string
	nodeEpoch map[string]string
}

func newMemoryNodeStore() *memoryNodeStore {
	return &memoryNodeStore{
		bindings:  make(map[string]string),
		nodeEpoch: make(map[string]string),
	}
}

func (*memoryNodeStore) Ping(context.Context) error { return nil }

func (l *memoryNodeStore) BindNode(_ context.Context, serviceName, uid, nodeID, epoch string) error {
	if !locate.ValidNodeLocation(serviceName, uid, nodeID) {
		return locate.ErrInvalidNodeBinding
	}
	if epoch == "" {
		return locate.ErrInvalidNodeEpoch
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.checkEpochLocked(serviceName, nodeID, epoch); err != nil {
		return err
	}
	l.bindings[serviceName+"\x00"+uid] = nodeID
	return nil
}

func (l *memoryNodeStore) LocateNode(_ context.Context, serviceName, uid string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	nodeID, ok := l.bindings[serviceName+"\x00"+uid]
	if !ok {
		return "", locate.ErrNodeNotFound
	}
	return nodeID, nil
}

func (l *memoryNodeStore) RenewNode(_ context.Context, serviceName, uid, nodeID, epoch string) error {
	if !locate.ValidNodeLocation(serviceName, uid, nodeID) {
		return locate.ErrInvalidNodeBinding
	}
	if epoch == "" {
		return locate.ErrInvalidNodeEpoch
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.checkEpochLocked(serviceName, nodeID, epoch); err != nil {
		return err
	}
	current, ok := l.bindings[serviceName+"\x00"+uid]
	if !ok {
		return locate.ErrNodeNotFound
	}
	if current != nodeID {
		return locate.ErrNodeConflict
	}
	return nil
}

func (l *memoryNodeStore) UnbindNode(_ context.Context, serviceName, uid, nodeID, epoch string) error {
	if !locate.ValidNodeLocation(serviceName, uid, nodeID) {
		return locate.ErrInvalidNodeBinding
	}
	if epoch == "" {
		return locate.ErrInvalidNodeEpoch
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.checkEpochLocked(serviceName, nodeID, epoch); err != nil {
		return err
	}
	key := serviceName + "\x00" + uid
	if l.bindings[key] == nodeID {
		delete(l.bindings, key)
	}
	return nil
}

func (l *memoryNodeStore) RegisterNodeEpoch(_ context.Context, serviceName, nodeID, epoch string, ttl time.Duration) error {
	if !locate.ValidServiceName(serviceName) || nodeID == "" || epoch == "" || ttl < time.Millisecond {
		return locate.ErrInvalidNodeEpoch
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	key := serviceName + "\x00" + nodeID
	if _, exists := l.nodeEpoch[key]; exists {
		return locate.ErrNodeEpochConflict
	}
	l.nodeEpoch[key] = epoch
	return nil
}

func (l *memoryNodeStore) RenewNodeEpoch(_ context.Context, serviceName, nodeID, epoch string, ttl time.Duration) error {
	if !locate.ValidServiceName(serviceName) || nodeID == "" || epoch == "" || ttl < time.Millisecond {
		return locate.ErrInvalidNodeEpoch
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.checkEpochLocked(serviceName, nodeID, epoch)
}

func (l *memoryNodeStore) LocateNodeEpoch(_ context.Context, serviceName, nodeID string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	epoch, ok := l.nodeEpoch[serviceName+"\x00"+nodeID]
	if !ok {
		return "", locate.ErrNodeEpochNotFound
	}
	return epoch, nil
}

func (l *memoryNodeStore) UnregisterNodeEpoch(_ context.Context, serviceName, nodeID, epoch string) error {
	if !locate.ValidServiceName(serviceName) || nodeID == "" || epoch == "" {
		return locate.ErrInvalidNodeEpoch
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	key := serviceName + "\x00" + nodeID
	if current, ok := l.nodeEpoch[key]; ok && current == epoch {
		delete(l.nodeEpoch, key)
	}
	return nil
}

func (l *memoryNodeStore) checkEpochLocked(serviceName, nodeID, epoch string) error {
	current, ok := l.nodeEpoch[serviceName+"\x00"+nodeID]
	if !ok {
		return locate.ErrNodeEpochNotFound
	}
	if current != epoch {
		return locate.ErrNodeEpochConflict
	}
	return nil
}

type controlledEpochLocator struct {
	Store
	mu    sync.RWMutex
	renew func(context.Context) error
}

func (l *controlledEpochLocator) setRenew(renew func(context.Context) error) {
	l.mu.Lock()
	l.renew = renew
	l.mu.Unlock()
}

func (l *controlledEpochLocator) RenewNodeEpoch(ctx context.Context, service, nodeID, epoch string, ttl time.Duration) error {
	l.mu.RLock()
	renew := l.renew
	l.mu.RUnlock()
	if renew != nil {
		return renew(ctx)
	}
	return l.Store.RenewNodeEpoch(ctx, service, nodeID, epoch, ttl)
}

type nodeTestAppInfo struct {
	id       string
	name     string
	metadata map[string]string
}

func (a nodeTestAppInfo) ID() string { return a.id }

func (a nodeTestAppInfo) Name() string { return a.name }

func (nodeTestAppInfo) Version() string { return "" }

func (a nodeTestAppInfo) Metadata() map[string]string { return a.metadata }

func (nodeTestAppInfo) Endpoint() []string { return nil }

type countingRegistrar struct {
	registered atomic.Int32
}

func (r *countingRegistrar) Register(context.Context, *registry.ServiceInstance) error {
	r.registered.Add(1)
	return nil
}

func (*countingRegistrar) Deregister(context.Context, *registry.ServiceInstance) error { return nil }

type lifecycleRegistrar struct {
	mu           sync.Mutex
	record       *registry.ServiceInstance
	entered      chan *registry.ServiceInstance
	register     func(context.Context, *registry.ServiceInstance) error
	deregister   func(context.Context, *registry.ServiceInstance) error
	deregistered atomic.Int32
}

func newLifecycleRegistrar() *lifecycleRegistrar {
	return &lifecycleRegistrar{entered: make(chan *registry.ServiceInstance, 1)}
}

func (r *lifecycleRegistrar) Register(ctx context.Context, record *registry.ServiceInstance) error {
	r.mu.Lock()
	r.record = record
	r.mu.Unlock()
	r.entered <- record
	if r.register != nil {
		return r.register(ctx, record)
	}
	return nil
}

func (r *lifecycleRegistrar) Deregister(ctx context.Context, record *registry.ServiceInstance) error {
	r.deregistered.Add(1)
	if r.deregister != nil {
		return r.deregister(ctx, record)
	}
	return nil
}

func (r *lifecycleRegistrar) instance() *registry.ServiceInstance {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.record
}

func tcpAddressReusable(address string) bool {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return false
	}
	_ = listener.Close()
	return true
}
