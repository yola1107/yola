package gateway

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yola/api/cluster/v1"
	protocolv1 "yola/api/protocol/v1"
	"yola/instance"
	"yola/locate"
	locateredis "yola/locate/redis"
	"yola/network"
	"yola/node"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-kratos/kratos/v3"
	"github.com/go-kratos/kratos/v3/registry"
	"github.com/go-kratos/kratos/v3/transport"
	kgrpc "github.com/go-kratos/kratos/v3/transport/grpc"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

func newTestServer(t testing.TB, opts ...Option) *Server {
	t.Helper()
	opts = append([]Option{
		Auth(testAuthenticator{}),
		Locator(pingLocator{}),
		Discovery(staticDiscovery{}),
	}, opts...)
	server, err := NewServer(opts...)
	require.NoError(t, err)
	return server
}

func newTestGateway(t testing.TB, store locate.Locator, grpcEndpoint string, opts ...Option) *Server {
	t.Helper()
	opts = append([]Option{
		Auth(testAuthenticator{}),
		Locator(store),
		Discovery(staticDiscovery{"game": {
			serviceInstance("game", "node-a", grpcEndpoint),
		}}),
		LeaseTTL(time.Minute),
	}, opts...)
	return newTestServer(t, opts...)
}

type watchDiscovery struct {
	instances []*registry.ServiceInstance
}

type watchDiscoveryWatcher struct {
	ctx       context.Context
	discovery *watchDiscovery
	first     bool
}

type pingLocator struct {
	locate.Locator
}

func (pingLocator) Ping(context.Context) error { return nil }

func newWatchDiscovery(instances ...*registry.ServiceInstance) *watchDiscovery {
	return &watchDiscovery{instances: instances}
}

func (d *watchDiscovery) GetService(_ context.Context, _ string) ([]*registry.ServiceInstance, error) {
	return append([]*registry.ServiceInstance(nil), d.instances...), nil
}

func (d *watchDiscovery) Watch(ctx context.Context, _ string) (registry.Watcher, error) {
	return &watchDiscoveryWatcher{ctx: ctx, discovery: d}, nil
}

func (w *watchDiscoveryWatcher) Next() ([]*registry.ServiceInstance, error) {
	if !w.first {
		w.first = true
		return w.discovery.GetService(w.ctx, "")
	}
	<-w.ctx.Done()
	return nil, w.ctx.Err()
}

func (*watchDiscoveryWatcher) Stop() error { return nil }

type testAuthenticator struct {
	remoteIPs chan string
	services  chan string
	deadlines chan bool
	calls     *atomic.Int32
}

type staticDiscovery map[string][]*registry.ServiceInstance

type testAppInfo struct {
	id        string
	name      string
	endpoints []string
}

func (a testAppInfo) ID() string                { return a.id }
func (a testAppInfo) Name() string              { return a.name }
func (testAppInfo) Version() string             { return "" }
func (testAppInfo) Metadata() map[string]string { return nil }
func (a testAppInfo) Endpoint() []string        { return a.endpoints }

func (a testAuthenticator) Authenticate(ctx context.Context, serviceName string, token []byte, remoteIP string) (string, error) {
	if a.calls != nil {
		a.calls.Add(1)
	}
	if a.services != nil {
		a.services <- serviceName
	}
	if a.remoteIPs != nil {
		a.remoteIPs <- remoteIP
	}
	if a.deadlines != nil {
		_, ok := ctx.Deadline()
		a.deadlines <- ok
	}
	if bytes.Equal(token, []byte("synthetic-token")) {
		return "player-a", nil
	}
	return "", context.Canceled
}

func (d staticDiscovery) GetService(_ context.Context, service string) ([]*registry.ServiceInstance, error) {
	return append([]*registry.ServiceInstance(nil), d[service]...), nil
}

func (d staticDiscovery) Watch(ctx context.Context, service string) (registry.Watcher, error) {
	return newWatchDiscovery(d[service]...).Watch(ctx, service)
}

func serviceInstance(service, id, endpoint string) *registry.ServiceInstance {
	return &registry.ServiceInstance{
		ID:        id,
		Name:      service,
		Metadata:  instance.StickyMetadata(),
		Endpoints: []string{endpoint},
	}
}

func backendClient(ctx context.Context, services *backends, serviceName string) (v1.NodeClient, error) {
	nodeBackend, err := services.get(ctx, serviceName)
	if err != nil {
		return nil, err
	}
	return nodeBackend.client, nil
}

type backendTestDiscovery struct {
	mu            sync.Mutex
	current       map[string][]*registry.ServiceInstance
	watchers      map[string]map[*backendTestWatcher]struct{}
	watchFailures int
	watchCalls    int
}

type backendTestWatcher struct {
	ctx       context.Context
	discovery *backendTestDiscovery
	service   string
	updates   chan []*registry.ServiceInstance
	stopOnce  sync.Once
}

func requireBackendServerExit(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		require.ErrorIs(t, err, grpc.ErrServerStopped)
	}
}

func newBackendTestDiscovery(instances ...*registry.ServiceInstance) *backendTestDiscovery {
	d := &backendTestDiscovery{
		current:  make(map[string][]*registry.ServiceInstance),
		watchers: make(map[string]map[*backendTestWatcher]struct{}),
	}
	for _, service := range instances {
		d.current[service.Name] = append(d.current[service.Name], service)
	}
	return d
}

func (d *backendTestDiscovery) GetService(_ context.Context, service string) ([]*registry.ServiceInstance, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]*registry.ServiceInstance(nil), d.current[service]...), nil
}

func (d *backendTestDiscovery) Watch(ctx context.Context, service string) (registry.Watcher, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.watchCalls++
	if d.watchFailures > 0 {
		d.watchFailures--
		return nil, errors.New("discovery unavailable")
	}
	watcher := &backendTestWatcher{
		ctx: ctx, discovery: d, service: service,
		updates: make(chan []*registry.ServiceInstance, 16),
	}
	if d.watchers[service] == nil {
		d.watchers[service] = make(map[*backendTestWatcher]struct{})
	}
	d.watchers[service][watcher] = struct{}{}
	watcher.updates <- append([]*registry.ServiceInstance(nil), d.current[service]...)
	return watcher, nil
}

func (d *backendTestDiscovery) failNextWatches(count int) {
	d.mu.Lock()
	d.watchFailures = count
	d.mu.Unlock()
}

func (d *backendTestDiscovery) watchCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.watchCalls
}

func (d *backendTestDiscovery) publish(service string, instances ...*registry.ServiceInstance) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.current[service] = append([]*registry.ServiceInstance(nil), instances...)
	for watcher := range d.watchers[service] {
		watcher.updates <- append([]*registry.ServiceInstance(nil), instances...)
	}
}

func (w *backendTestWatcher) Next() ([]*registry.ServiceInstance, error) {
	select {
	case instances := <-w.updates:
		return instances, nil
	case <-w.ctx.Done():
		return nil, w.ctx.Err()
	}
}

func (w *backendTestWatcher) Stop() error {
	w.stopOnce.Do(func() {
		w.discovery.mu.Lock()
		delete(w.discovery.watchers[w.service], w)
		w.discovery.mu.Unlock()
	})
	return nil
}

type testGameService struct{}

func (testGameService) enter(_ context.Context, body []byte) ([]byte, error) {
	return append([]byte("node:"), body...), nil
}

func (testGameService) enterAndPush(ctx context.Context, body []byte) ([]byte, error) {
	sess, _ := node.FromContext(ctx)
	if err := sess.Push(ctx, 3, &protocolv1.Proto{Body: body}); err != nil {
		return nil, err
	}
	return append([]byte("node:"), body...), nil
}

func (testGameService) enterAndBind(ctx context.Context, body []byte) ([]byte, error) {
	sess, _ := node.FromContext(ctx)
	if err := sess.BindNode(ctx); err != nil {
		return nil, err
	}
	return append([]byte("node:"), body...), nil
}

type testConnection struct {
	connID         string
	closed         chan struct{}
	once           sync.Once
	pushes         chan *protocolv1.Proto
	sendErr        error
	closeWithProto func(context.Context, *protocolv1.Proto) error
}

func newTestConnection(connID string) *testConnection {
	return &testConnection{
		connID: connID,
		closed: make(chan struct{}),
		pushes: make(chan *protocolv1.Proto, 1),
	}
}

func (c *testConnection) ConnID() string     { return c.connID }
func (c *testConnection) RemoteAddr() string { return "127.0.0.1:5000" }
func (c *testConnection) SendProto(msg *protocolv1.Proto) error {
	if c.sendErr != nil {
		return c.sendErr
	}
	c.pushes <- proto.Clone(msg).(*protocolv1.Proto)
	return nil
}

func (c *testConnection) CloseWithProto(ctx context.Context, msg *protocolv1.Proto) error {
	if c.closeWithProto != nil {
		return c.closeWithProto(ctx, msg)
	}
	if c.sendErr != nil {
		return c.sendErr
	}
	cloned := proto.Clone(msg).(*protocolv1.Proto)
	select {
	case c.pushes <- cloned:
	case <-ctx.Done():
		_ = c.Close()
		return ctx.Err()
	default:
		// Drop if the test already left an unread frame; shutdown must not block.
	}
	return c.Close()
}

func (c *testConnection) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func testBinding() locate.GateBinding {
	return locate.GateBinding{
		ServiceName: "game", UID: "player-a", BindingToken: "binding-a",
		GateID: "gate-a", GateEndpoint: "grpc://127.0.0.1:9100", ConnID: "conn-a",
	}
}

func activeSession(conn network.Connection, binding locate.GateBinding) *session {
	return &session{
		conn:          conn,
		binding:       binding,
		leaseDeadline: time.Now().Add(time.Minute),
	}
}

func isClosed(done <-chan struct{}) func() bool {
	return func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}
}

func receiveWithin[T any](t *testing.T, values <-chan T) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for test synchronization")
		var zero T
		return zero
	}
}

func authMessage(t *testing.T) *protocolv1.Proto {
	return authMessageForService(t, "game")
}

func authMessageForService(t *testing.T, service string) *protocolv1.Proto {
	t.Helper()
	body, err := proto.Marshal(&protocolv1.ClientAuthReq{ServiceName: service, Token: []byte("synthetic-token")})
	require.NoError(t, err)
	return &protocolv1.Proto{Op: protocolv1.OpAuth, Seq: 1, Body: body}
}

func testLocator(t *testing.T) locate.Locator {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	return locateredis.New(client)
}

func bindTestPlayerNode(t *testing.T, store locate.Locator, serviceName, uid, nodeID string) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, store.RegisterNodeEpoch(ctx, serviceName, nodeID, "test-epoch-"+nodeID, node.DefaultNodeEpochTTL))
	require.NoError(t, store.BindNode(ctx, serviceName, uid, nodeID))
}

func startTestNode(t *testing.T) string {
	t.Helper()
	return startTestNodeInstance(t, "node-a", "game")
}

func startTestNodeInstance(t *testing.T, id, service string) string {
	endpoint, stop := startTestNodeServer(t, id, service, false)
	t.Cleanup(stop)
	return endpoint
}

func startTestNodeServer(t *testing.T, id string, serviceName string, sticky bool, opts ...node.Option) (string, func()) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	opts = append(opts, node.Listener(lis))
	ns, err := node.NewServer(opts...)
	require.NoError(t, err)
	serviceImpl := testGameService{}
	ns.RegisterRawHandler(1, serviceImpl.enter)
	ns.RegisterRawHandler(2, serviceImpl.enterAndPush)
	ns.RegisterRawHandler(3, serviceImpl.enterAndBind)
	appOptions := []kratos.Option{
		kratos.ID(id), kratos.Name(serviceName),
	}
	if sticky {
		appOptions = append(appOptions, kratos.Metadata(instance.StickyMetadata()))
	}
	appOptions = append(appOptions, kratos.BeforeStart(ns.BeforeStart), kratos.Server(ns))
	app := kratos.New(appOptions...)
	grpcDone := make(chan error, 1)
	go func() { grpcDone <- app.Run() }()
	probe, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	health := healthpb.NewHealthClient(probe)
	require.Eventually(t, func() bool {
		probeCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		reply, err := health.Check(probeCtx, &healthpb.HealthCheckRequest{})
		return err == nil && reply.Status == healthpb.HealthCheckResponse_SERVING
	}, time.Second, time.Millisecond)
	require.NoError(t, probe.Close())
	return "grpc://" + lis.Addr().String(), sync.OnceFunc(func() {
		_ = app.Stop()
		<-grpcDone
	})
}

func initTestGateway(t *testing.T, gateway *Server) {
	t.Helper()
	ctx := kratos.NewContext(context.Background(), testAppInfo{
		id:        "gate-a",
		name:      "gate",
		endpoints: []string{"grpc://127.0.0.1:9100"},
	})
	require.NoError(t, gateway.BeforeStart(ctx))
	started := make(chan error, 1)
	go func() { started <- gateway.Start(ctx) }()
	require.Eventually(t, gateway.admission.accepting.Load, time.Second, time.Millisecond)
	t.Cleanup(func() {
		_ = gateway.Stop(context.Background())
		require.NoError(t, <-started)
	})
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

type stubTransport struct {
	handlerErr error
	prepareErr error
	prepare    func(context.Context) error
	start      func(context.Context) error
	stop       func(context.Context) error
}

func (t *stubTransport) Start(ctx context.Context) error {
	if t.start == nil {
		return nil
	}
	return t.start(ctx)
}

func (t *stubTransport) Stop(ctx context.Context) error {
	if t.stop == nil {
		return nil
	}
	return t.stop(ctx)
}

func (t *stubTransport) BeforeStart(ctx context.Context) error {
	if t.prepare != nil {
		return t.prepare(ctx)
	}
	return t.prepareErr
}

func (t *stubTransport) SetHandler(network.ConnectionHandler) error { return t.handlerErr }

func startTestApp(t *testing.T, gateway *Server, servers ...transport.Server) {
	t.Helper()
	app := newTestApp(gateway, servers...)
	serverDone := make(chan error, 1)
	go func() { serverDone <- app.Run() }()
	t.Cleanup(func() {
		require.NoError(t, app.Stop())
		require.NoError(t, <-serverDone)
	})
	require.Eventually(t, gateway.admission.accepting.Load, time.Second, time.Millisecond)
}

func newTestApp(gateway *Server, servers ...transport.Server) *kratos.App {
	opts := []kratos.Option{
		kratos.ID("gate-a"),
		kratos.Name("gateway"),
		kratos.StopTimeout(time.Second),
		kratos.BeforeStart(gateway.BeforeStart),
		kratos.Server(append([]transport.Server{gateway}, servers...)...),
	}
	return kratos.New(opts...)
}

func forwardBackend(client v1.NodeClient) error {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := client.Forward(ctx, &v1.ForwardRequest{
		Route: &v1.GateRoute{
			ServiceName:  "game",
			Uid:          "player-a",
			BindingToken: "binding-a",
			GateId:       "gate-a",
			GateEndpoint: "grpc://127.0.0.1:9100",
			ConnId:       "conn-a",
		},
		Command: 1,
		Body:    []byte("request"),
	})
	return err
}

func newDisconnectTrackingServer(t *testing.T) (*Server, locate.Locator, *testConnection, locate.GateBinding, <-chan *v1.DisconnectRequest) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := grpc.NewServer()
	disconnects := make(chan *v1.DisconnectRequest, 1)
	service := &forwardServer{disconnects: disconnects}
	v1.RegisterNodeServer(server, service)
	go func() { _ = server.Serve(lis) }()
	t.Cleanup(server.Stop)

	store := testLocator(t)
	backendPool := newBackends(staticDiscovery{"game": {
		serviceInstance("game", "node-a", "grpc://"+lis.Addr().String()),
	}}, nil, time.Second)
	t.Cleanup(backendPool.close)
	binding := testBinding()
	gateway := &Server{
		identity:   identity{id: binding.GateID, endpoint: binding.GateEndpoint},
		locator:    store,
		backends:   backendPool,
		rpcTimeout: time.Second,
		sessions:   &sessionRegistry{byConnID: make(map[string]*session)},
	}
	conn := newTestConnection("conn-a")
	_, _, err = store.BindGate(context.Background(), binding, time.Minute)
	require.NoError(t, err)
	require.True(t, gateway.sessions.add(conn, time.Minute))
	sess := gateway.sessions.get(conn.ConnID())
	require.NotNil(t, sess)
	require.True(t, sess.finishAuthentication(locate.GateLease{Binding: binding, TTL: time.Minute}, time.Now()))
	return gateway, store, conn, binding, disconnects
}

type forwardServer struct {
	v1.UnimplementedNodeServer
	request     *v1.ForwardRequest
	body        []byte
	err         error
	disconnects chan *v1.DisconnectRequest
}

func (s *forwardServer) Forward(_ context.Context, in *v1.ForwardRequest) (*v1.ForwardReply, error) {
	s.request = in
	if s.err != nil {
		return nil, s.err
	}
	body := s.body
	if body == nil {
		body = []byte("reply")
	}
	return &v1.ForwardReply{Body: body}, nil
}

func (s *forwardServer) Disconnect(_ context.Context, in *v1.DisconnectRequest) (*emptypb.Empty, error) {
	if s.disconnects != nil {
		s.disconnects <- in
	}
	return &emptypb.Empty{}, nil
}

type blockingBindLocator struct {
	locate.Locator
	entered     chan locate.GateBinding
	release     chan struct{}
	releaseOnce sync.Once
}

func (s *blockingBindLocator) unblock() {
	s.releaseOnce.Do(func() { close(s.release) })
}

func newBlockingBindLocator(store locate.Locator) *blockingBindLocator {
	return &blockingBindLocator{
		Locator: store, entered: make(chan locate.GateBinding, 1), release: make(chan struct{}),
	}
}

func (s *blockingBindLocator) BindGate(ctx context.Context, binding locate.GateBinding, ttl time.Duration) (locate.GateLease, *locate.GateBinding, error) {
	lease, previous, err := s.Locator.BindGate(ctx, binding, ttl)
	s.entered <- binding
	<-s.release
	return lease, previous, err
}

type blockingUnbindLocator struct {
	locate.Locator
	called chan context.Context
}

func (s *blockingUnbindLocator) UnbindGate(ctx context.Context, _ locate.GateBinding) error {
	s.called <- ctx
	<-ctx.Done()
	return ctx.Err()
}

type namedBackendNode struct {
	v1.UnimplementedNodeServer
	name      string
	deadlines chan<- bool
}

func (n *namedBackendNode) Forward(ctx context.Context, in *v1.ForwardRequest) (*v1.ForwardReply, error) {
	if n.deadlines != nil {
		_, hasDeadline := ctx.Deadline()
		select {
		case n.deadlines <- hasDeadline:
		default:
		}
	}
	return &v1.ForwardReply{Body: []byte(n.name + ":" + in.GetNodeId())}, nil
}

func startNamedBackendNode(t *testing.T, name string) string {
	t.Helper()
	return startBackendNode(t, &namedBackendNode{name: name})
}

func startBackendNode(t *testing.T, node v1.NodeServer) string {
	t.Helper()
	return startBackendNodeWithOptions(t, "grpc", node, kgrpc.Timeout(0))
}

func startBackendNodeWithOptions(t *testing.T, scheme string, node v1.NodeServer, options ...kgrpc.ServerOption) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	options = append([]kgrpc.ServerOption{kgrpc.Listener(lis)}, options...)
	server := kgrpc.NewServer(options...)
	v1.RegisterNodeServer(server, node)
	done := make(chan error, 1)
	go func() { done <- server.Start(context.Background()) }()
	t.Cleanup(func() {
		require.NoError(t, server.Stop(context.Background()))
		requireBackendServerExit(t, <-done)
	})
	return scheme + "://" + lis.Addr().String()
}
