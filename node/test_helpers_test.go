package node

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"yola/locate"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func newTestServer(t testing.TB, opts ...Option) *Server {
	t.Helper()
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

func publishTestIdentity(server *Server, identity nodeIdentity) {
	server.lifecycleMu.Lock()
	defer server.lifecycleMu.Unlock()
	server.identity.Store(&identity)
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

func assertHandlerRegistrationFrozen(t *testing.T, server *Server) {
	t.Helper()
	require.PanicsWithValue(t, "node: handlers must be registered before BeforeStart", func() {
		server.RegisterRawHandler(1, func(context.Context, []byte) ([]byte, error) { return nil, nil })
	})
	require.PanicsWithValue(t, "node: handlers must be registered before BeforeStart", func() {
		server.OnDisconnect(func(context.Context, Session) error { return nil })
	})
}

func (s *Server) forward(ctx context.Context, binding locate.GateBinding, command int32, body []byte) ([]byte, error) {
	return s.forwardTo(ctx, stickyClaim{}, binding, command, body)
}

// memoryLocator 模拟 Node 绑定、epoch 和 Push 所需的 Gate 查询，不模拟 TTL 流逝。
// Gate 续租、解绑不属于 Node 职责，意外调用会使测试立即失败。
type memoryLocator struct {
	mu        sync.Mutex
	bindings  map[string]string
	nodeEpoch map[string]string
	gateways  map[string]locate.GateLease
}

func newMemoryLocator() *memoryLocator {
	return &memoryLocator{
		bindings:  make(map[string]string),
		nodeEpoch: make(map[string]string),
		gateways:  make(map[string]locate.GateLease),
	}
}

func (*memoryLocator) Ping(context.Context) error { return nil }

func (l *memoryLocator) BindNode(_ context.Context, serviceName, uid, nodeID, epoch string) error {
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

func (l *memoryLocator) LocateNode(_ context.Context, serviceName, uid string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	nodeID, ok := l.bindings[serviceName+"\x00"+uid]
	if !ok {
		return "", locate.ErrNodeNotFound
	}
	return nodeID, nil
}

func (l *memoryLocator) RenewNode(_ context.Context, serviceName, uid, nodeID, epoch string) error {
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

func (l *memoryLocator) UnbindNode(_ context.Context, serviceName, uid, nodeID, epoch string) error {
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

func (l *memoryLocator) RegisterNodeEpoch(_ context.Context, serviceName, nodeID, epoch string, ttl time.Duration) error {
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

func (l *memoryLocator) RenewNodeEpoch(_ context.Context, serviceName, nodeID, epoch string, ttl time.Duration) error {
	if !locate.ValidServiceName(serviceName) || nodeID == "" || epoch == "" || ttl < time.Millisecond {
		return locate.ErrInvalidNodeEpoch
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.checkEpochLocked(serviceName, nodeID, epoch)
}

func (l *memoryLocator) checkEpochLocked(serviceName, nodeID, epoch string) error {
	current, ok := l.nodeEpoch[serviceName+"\x00"+nodeID]
	if !ok {
		return locate.ErrNodeEpochNotFound
	}
	if current != epoch {
		return locate.ErrNodeEpochConflict
	}
	return nil
}

func (l *memoryLocator) LocateNodeEpoch(_ context.Context, serviceName, nodeID string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	epoch, ok := l.nodeEpoch[serviceName+"\x00"+nodeID]
	if !ok {
		return "", locate.ErrNodeEpochNotFound
	}
	return epoch, nil
}

func (l *memoryLocator) UnregisterNodeEpoch(_ context.Context, serviceName, nodeID, epoch string) error {
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

func (l *memoryLocator) BindGate(_ context.Context, candidate locate.GateBinding, ttl time.Duration) (locate.GateLease, *locate.GateBinding, error) {
	if !locate.ValidGateBinding(candidate) {
		return locate.GateLease{}, nil, locate.ErrInvalidGateBinding
	}
	if ttl < time.Millisecond {
		return locate.GateLease{}, nil, locate.ErrInvalidGateTTL
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	key := candidate.ServiceName + "\x00" + candidate.UID
	var previous *locate.GateBinding
	if current, ok := l.gateways[key]; ok {
		prev := current.Binding
		previous = &prev
	}
	lease := locate.GateLease{Binding: candidate, TTL: ttl.Truncate(time.Millisecond)}
	l.gateways[key] = lease
	return lease, previous, nil
}

func (l *memoryLocator) LocateGate(_ context.Context, serviceName, uid string) (locate.GateLease, error) {
	if !locate.ValidServiceName(serviceName) || !locate.ValidUID(uid) {
		return locate.GateLease{}, locate.ErrInvalidGateBinding
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	lease, ok := l.gateways[serviceName+"\x00"+uid]
	if !ok {
		return locate.GateLease{}, locate.ErrGateNotFound
	}
	return lease, nil
}

func (*memoryLocator) RenewGateLease(context.Context, locate.GateBinding, time.Duration) (locate.GateLease, error) {
	panic("node test: unexpected RenewGateLease call")
}

func (*memoryLocator) UnbindGate(context.Context, locate.GateBinding) error {
	panic("node test: unexpected UnbindGate call")
}

var _ locate.Locator = (*memoryLocator)(nil)

func testBinding(uid, connID string) locate.GateBinding {
	return locate.GateBinding{
		ServiceName: "game", UID: uid, BindingToken: "binding-" + uid,
		GateID: "gate-a", GateEndpoint: "grpc://127.0.0.1:9000", ConnID: connID,
	}
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

type nodeTestAppInfo struct {
	metadata map[string]string
}

func (nodeTestAppInfo) ID() string { return "node-a" }

func (nodeTestAppInfo) Name() string { return "game" }

func (nodeTestAppInfo) Version() string { return "" }

func (a nodeTestAppInfo) Metadata() map[string]string { return a.metadata }

func (nodeTestAppInfo) Endpoint() []string { return nil }

func savedBindingSession(t *testing.T, locator locate.Locator, opts ...Option) (*Server, Session) {
	t.Helper()
	opts = append(opts, Locator(locator))
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

func waitNodeSignal(t testing.TB, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Node lifecycle signal")
	}
}
