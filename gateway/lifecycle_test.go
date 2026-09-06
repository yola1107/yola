package gateway

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	protocolv1 "yola/api/protocol/v1"
	"yola/locate"
	"yola/network/tcp"

	"github.com/go-kratos/kratos/v3"
	"github.com/go-kratos/kratos/v3/registry"
	"github.com/stretchr/testify/require"
)

func TestBeforeStartUsesInternalGRPCEndpoint(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	want := &url.URL{Scheme: "grpc", Host: lis.Addr().String()}
	server := newTestGateway(
		t, testLocator(t), "grpc://127.0.0.1:1",
		Listener(lis), Endpoint(want),
	)
	t.Cleanup(func() { require.NoError(t, server.Stop(context.Background())) })
	ctx := kratos.NewContext(context.Background(), testAppInfo{
		id: "gate-a", name: "gateway", endpoints: []string{"grpc://127.0.0.1:2", want.String()},
	})

	require.NoError(t, server.BeforeStart(ctx))
	require.Equal(t, want.String(), server.identity.endpoint)
}

func TestBeforeStartFailureReleasesInternalGRPCListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	server := newTestServer(t, Listener(listener))
	_, err = server.Endpoint()
	require.NoError(t, err)

	ctx := kratos.NewContext(context.Background(), testAppInfo{name: "gateway"})
	require.EqualError(t, server.BeforeStart(ctx), "gateway: application ID is required")
	rebound, err := net.Listen("tcp", address)
	require.NoError(t, err)
	require.NoError(t, rebound.Close())
	require.NoError(t, server.Stop(context.Background()))
}

func TestBeforeStartUsesCallerContext(t *testing.T) {
	pinged := make(chan context.Context, 1)
	gate := newTestServer(t, Locator(&initializationLocator{
		Locator: testLocator(t), pinged: pinged,
	}))
	baseCtx, cancel := context.WithCancel(context.Background())
	ctx := kratos.NewContext(baseCtx, testAppInfo{
		id: "gate-a", name: "gate", endpoints: []string{"grpc://127.0.0.1:9100"},
	})
	prepared := make(chan error, 1)
	go func() { prepared <- gate.BeforeStart(ctx) }()
	preparationCtx := receiveWithin(t, pinged)
	require.EqualError(t, gate.BeforeStart(ctx), "gateway: server is already preparing or prepared")
	gate.lifecycle.mu.Lock()
	require.Equal(t, stPreparing, gate.lifecycle.state)
	gate.lifecycle.mu.Unlock()
	cancel()
	require.ErrorIs(t, preparationCtx.Err(), context.Canceled)
	require.ErrorIs(t, <-prepared, context.Canceled)
	require.NoError(t, gate.Stop(context.Background()))
}

func TestBeforeStartReentryMakesPreparedServerTerminal(t *testing.T) {
	var stopCalls atomic.Int32
	gate := newTestServer(t, Transport(&stubTransport{stop: func(context.Context) error {
		stopCalls.Add(1)
		return nil
	}}))
	t.Cleanup(func() { require.NoError(t, gate.Stop(context.Background())) })
	ctx := kratos.NewContext(context.Background(), testAppInfo{id: "gate-a", name: "gateway"})

	require.NoError(t, gate.BeforeStart(ctx))
	gate.lifecycle.mu.Lock()
	require.Equal(t, stPrepared, gate.lifecycle.state)
	gate.lifecycle.mu.Unlock()
	require.EqualError(t, gate.BeforeStart(ctx), "gateway: server is already preparing or prepared")
	gate.lifecycle.mu.Lock()
	require.Equal(t, stStopping, gate.lifecycle.state)
	gate.lifecycle.mu.Unlock()
	require.Zero(t, stopCalls.Load())
	require.EqualError(t, gate.Start(ctx), "gateway: server is stopping or stopped")

	require.NoError(t, gate.Stop(context.Background()))
	require.Equal(t, int32(1), stopCalls.Load())
}

func TestStopWaitsForPreparationRollback(t *testing.T) {
	prepareStarted := make(chan struct{})
	releasePrepare := make(chan struct{})
	rollbackStarted := make(chan struct{})
	releaseRollback := make(chan struct{})
	releasePreparation := sync.OnceFunc(func() { close(releasePrepare) })
	releaseCleanup := sync.OnceFunc(func() { close(releaseRollback) })
	var stopCalls atomic.Int32
	clientTransport := &stubTransport{
		prepare: func(context.Context) error {
			close(prepareStarted)
			<-releasePrepare
			return nil
		},
		stop: func(context.Context) error {
			if stopCalls.Add(1) == 1 {
				close(rollbackStarted)
				<-releaseRollback
			}
			return nil
		},
	}
	gate := newTestServer(t, Transport(clientTransport))
	t.Cleanup(func() {
		releasePreparation()
		releaseCleanup()
		_ = gate.shutdown(context.Background())
	})
	ctx := kratos.NewContext(context.Background(), testAppInfo{id: "gate-a", name: "gateway"})
	prepared := make(chan error, 1)
	go func() { prepared <- gate.BeforeStart(ctx) }()
	receiveWithin(t, prepareStarted)
	require.Equal(t, identity{}, gate.identity)

	stopped := make(chan error, 1)
	go func() { stopped <- gate.Stop(context.Background()) }()
	require.Eventually(t, func() bool {
		gate.lifecycle.mu.Lock()
		defer gate.lifecycle.mu.Unlock()
		return gate.lifecycle.state == stStopping
	}, time.Second, time.Millisecond)
	select {
	case err := <-stopped:
		t.Fatalf("Stop returned before preparation completed: %v", err)
	default:
	}

	releasePreparation()
	receiveWithin(t, rollbackStarted)
	select {
	case err := <-stopped:
		t.Fatalf("Stop returned before preparation rollback completed: %v", err)
	default:
	}
	releaseCleanup()
	require.EqualError(t, receiveWithin(t, prepared), "gateway: server is stopping or stopped")
	require.NoError(t, receiveWithin(t, stopped))
	require.GreaterOrEqual(t, stopCalls.Load(), int32(1))
	gate.lifecycle.mu.Lock()
	require.Equal(t, stStopping, gate.lifecycle.state)
	require.Equal(t, identity{}, gate.identity)
	gate.lifecycle.mu.Unlock()
}

func TestStopTimeoutLeavesPreparationOwnerToRollback(t *testing.T) {
	prepareStarted := make(chan struct{})
	releasePrepare := make(chan struct{})
	rolledBack := make(chan struct{})
	releasePreparation := sync.OnceFunc(func() { close(releasePrepare) })
	var rollbackOnce sync.Once
	clientTransport := &stubTransport{
		prepare: func(context.Context) error {
			close(prepareStarted)
			<-releasePrepare
			return nil
		},
		stop: func(context.Context) error {
			rollbackOnce.Do(func() { close(rolledBack) })
			return nil
		},
	}
	gate := newTestServer(t, Transport(clientTransport))
	t.Cleanup(func() {
		releasePreparation()
		_ = gate.shutdown(context.Background())
	})
	ctx := kratos.NewContext(context.Background(), testAppInfo{id: "gate-a", name: "gateway"})
	prepared := make(chan error, 1)
	go func() { prepared <- gate.BeforeStart(ctx) }()
	receiveWithin(t, prepareStarted)

	stopCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, gate.Stop(stopCtx), context.DeadlineExceeded)
	releasePreparation()
	require.EqualError(t, receiveWithin(t, prepared), "gateway: server is stopping or stopped")
	receiveWithin(t, rolledBack)
	gate.lifecycle.mu.Lock()
	require.Equal(t, stStopping, gate.lifecycle.state)
	require.Equal(t, identity{}, gate.identity)
	gate.lifecycle.mu.Unlock()
}

func TestGatewayCleansUpAfterTransportFailure(t *testing.T) {
	transportErr := errors.New("transport failed")
	grpcLis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	gate := newTestGateway(
		t, testLocator(t), "grpc://127.0.0.1:1",
		Listener(grpcLis),
		Transport(tcp.NewServer(tcp.Listener(&failingListener{acceptErr: transportErr}))),
	)
	app := newTestApp(gate)
	err = app.Run()
	require.ErrorIs(t, err, transportErr)
	require.False(t, gate.admission.accepting.Load())
	require.EqualError(t, gate.Start(context.Background()), "gateway: server is stopping or stopped")
	require.NoError(t, gate.Stop(context.Background()))
}

func TestStartLabelsClientAndGRPCTransportFailures(t *testing.T) {
	t.Run("client transport", func(t *testing.T) {
		startErr := errors.New("start failed")
		gate := newTestServer(t, Transport(
			&stubTransport{},
			&stubTransport{start: func(context.Context) error { return startErr }},
		))
		ctx := kratos.NewContext(context.Background(), testAppInfo{id: "gate-a", name: "gateway"})
		require.NoError(t, gate.BeforeStart(ctx))
		require.NoError(t, gate.grpcServer.Stop(context.Background()))

		err := gate.Start(ctx)
		require.ErrorIs(t, err, startErr)
		require.ErrorContains(t, err, "gateway: start client transport 1 (*gateway.stubTransport)")
		require.NoError(t, gate.Stop(context.Background()))
	})

	t.Run("gRPC transport", func(t *testing.T) {
		gate := newTestServer(t)
		ctx := kratos.NewContext(context.Background(), testAppInfo{id: "gate-a", name: "gateway"})
		require.NoError(t, gate.BeforeStart(ctx))
		require.NoError(t, gate.grpcListener.Close())

		err := gate.Start(ctx)
		require.Error(t, err)
		require.ErrorContains(t, err, "gateway: start gRPC transport")
		require.NoError(t, gate.Stop(context.Background()))
	})
}

func TestStopLabelsEachClientTransportFailure(t *testing.T) {
	firstErr := errors.New("first stop failed")
	secondErr := errors.New("second stop failed")
	gate := newTestServer(t, Transport(
		&stubTransport{stop: func(context.Context) error { return firstErr }},
		&stubTransport{stop: func(context.Context) error { return secondErr }},
	))
	ctx := kratos.NewContext(context.Background(), testAppInfo{id: "gate-a", name: "gateway"})
	require.NoError(t, gate.BeforeStart(ctx))

	err := gate.Stop(context.Background())
	require.ErrorIs(t, err, firstErr)
	require.ErrorIs(t, err, secondErr)
	require.ErrorContains(t, err, "gateway: stop client transport 0 (*gateway.stubTransport)")
	require.ErrorContains(t, err, "gateway: stop client transport 1 (*gateway.stubTransport)")
}

func TestDrainSessionsSendsShutdownKick(t *testing.T) {
	conn := newTestConnection("conn-shutdown")
	gate := &Server{
		sessions: &sessionRegistry{byConnID: map[string]*session{
			conn.ConnID(): {conn: conn},
		}},
		rpcTimeout: time.Second,
	}

	require.NoError(t, gate.drainSessions(context.Background()))

	require.True(t, isClosed(conn.closed)())
	require.Nil(t, gate.sessions.byConnID)
	require.Nil(t, gate.sessions.get(conn.ConnID()))
	require.False(t, gate.sessions.add(newTestConnection("conn-late"), time.Second))
	select {
	case msg := <-conn.pushes:
		require.Equal(t, protocolv1.OpKick, msg.Op)
		require.Equal(t, protocolv1.KickCodeServerShutdown, msg.Code)
	case <-time.After(time.Second):
		t.Fatal("expected shutdown OpKick")
	}
}

func TestQuiesceHonorsAdmissionDeadline(t *testing.T) {
	gate := newTestServer(t)
	gate.admission.wg.Add(1)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(gate.admission.wg.Done) }
	t.Cleanup(release)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	stopped := make(chan error, 1)
	go func() { stopped <- gate.quiesce(ctx) }()

	select {
	case err := <-stopped:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(200 * time.Millisecond):
		release()
		t.Fatal("quiesce ignored the admission deadline")
	}
}

func TestStopWaitsWhenCallerHasNoDeadline(t *testing.T) {
	gate := newTestServer(t)
	gate.admission.wg.Add(1)
	stopped := make(chan error, 1)
	go func() { stopped <- gate.Stop(context.Background()) }()
	select {
	case err := <-stopped:
		t.Fatalf("Stop returned without a caller deadline or admission release: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	gate.admission.wg.Done()
	require.NoError(t, receiveWithin(t, stopped))
}

func TestDrainSessionsRedistributesWorkFromBlockedWorker(t *testing.T) {
	const sessionCount = shutdownWorkerCount * 2
	blocked := make(chan struct{})
	release := make(chan struct{})
	releaseConnections := sync.OnceFunc(func() { close(release) })
	var claimedBlocker atomic.Bool
	var completed atomic.Int32
	registry := &sessionRegistry{byConnID: make(map[string]*session, sessionCount)}
	for index := range sessionCount {
		connID := "conn-" + strconv.Itoa(index)
		conn := newTestConnection(connID)
		conn.closeWithProto = func(context.Context, *protocolv1.Proto) error {
			if claimedBlocker.CompareAndSwap(false, true) {
				close(blocked)
				<-release
				return nil
			}
			completed.Add(1)
			return nil
		}
		registry.byConnID[connID] = &session{conn: conn}
	}
	gate := &Server{sessions: registry, rpcTimeout: time.Second}
	drained := make(chan struct{})
	drainResult := make(chan error, 1)
	go func() {
		drainResult <- gate.drainSessions(context.Background())
		close(drained)
	}()
	t.Cleanup(func() {
		releaseConnections()
		<-drained
	})
	receiveWithin(t, blocked)
	require.Eventually(t, func() bool {
		return completed.Load() == sessionCount-1
	}, time.Second, time.Millisecond)
	releaseConnections()
	receiveWithin(t, drained)
	require.NoError(t, <-drainResult)
}

func TestDrainSessionsPreservesShutdownDeadlineForCleanup(t *testing.T) {
	binding := testBinding()
	conn := newTestConnection(binding.ConnID)
	store := &blockingUnbindLocator{Locator: testLocator(t), called: make(chan context.Context, 1)}
	gate := &Server{
		locator:    store,
		backends:   newBackends(staticDiscovery{}, nil, 300*time.Millisecond),
		sessions:   &sessionRegistry{byConnID: map[string]*session{binding.ConnID: activeSession(conn, binding)}},
		rpcTimeout: 300 * time.Millisecond,
	}
	t.Cleanup(gate.backends.close)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	_ = gate.drainSessions(ctx)
	require.Less(t, time.Since(started), 150*time.Millisecond)
	cleanupCtx := receiveWithin(t, store.called)
	deadline, ok := cleanupCtx.Deadline()
	require.True(t, ok)
	require.LessOrEqual(t, deadline.Sub(started), 50*time.Millisecond)
}

func TestGatewayPreparationFailurePreventsRegistration(t *testing.T) {
	store := &failingPingLocator{Locator: testLocator(t)}
	gateway := newTestServer(t, Locator(store))
	t.Cleanup(func() { require.NoError(t, gateway.Stop(context.Background())) })
	registrar := new(countingRegistrar)
	app := kratos.New(
		kratos.ID("gate-a"),
		kratos.Name("gateway"),
		kratos.BeforeStart(gateway.BeforeStart),
		kratos.Server(gateway),
		kratos.Registrar(registrar),
	)

	require.EqualError(t, app.Run(), "transient ping failure")
	require.Zero(t, registrar.registered.Load())
}

func TestGatewayTransportPreparationFailurePreventsRegistration(t *testing.T) {
	prepareErr := errors.New("transport preparation failed")
	var cleanupErr error
	prepared := &stubTransport{stop: func(ctx context.Context) error {
		cleanupErr = ctx.Err()
		return nil
	}}
	firstRollback := true
	failing := &stubTransport{
		prepareErr: prepareErr,
		stop: func(ctx context.Context) error {
			if firstRollback {
				firstRollback = false
				<-ctx.Done()
				return ctx.Err()
			}
			return nil
		},
	}
	gate := newTestServer(t, RPCTimeout(20*time.Millisecond), Transport(prepared, failing))
	t.Cleanup(func() { require.NoError(t, gate.Stop(context.Background())) })
	registrar := new(countingRegistrar)
	app := kratos.New(
		kratos.ID("gate-a"),
		kratos.Name("gateway"),
		kratos.BeforeStart(gate.BeforeStart),
		kratos.Server(gate),
		kratos.Registrar(registrar),
	)

	err := app.Run()
	require.ErrorIs(t, err, prepareErr)
	require.ErrorContains(t, err, "gateway: prepare client transport 1 (*gateway.stubTransport)")
	require.ErrorContains(t, err, "gateway: roll back client transport 1 (*gateway.stubTransport)")
	require.NoError(t, cleanupErr)
	require.Zero(t, registrar.registered.Load())
	require.False(t, gate.admission.accepting.Load())
	require.EqualError(t, gate.Start(context.Background()), "gateway: server is stopping or stopped")
}

type initializationLocator struct {
	locate.Locator
	pinged chan<- context.Context
}

type failingPingLocator struct {
	locate.Locator
}

type countingRegistrar struct {
	registered atomic.Int32
}

type failingListener struct {
	acceptErr error
}

func (l *failingListener) Accept() (net.Conn, error) { return nil, l.acceptErr }
func (*failingListener) Close() error                { return nil }
func (*failingListener) Addr() net.Addr              { return &net.TCPAddr{IP: net.ParseIP("127.0.0.1")} }

func (s *initializationLocator) Ping(ctx context.Context) error {
	s.pinged <- ctx
	<-ctx.Done()
	return ctx.Err()
}

func (*failingPingLocator) Ping(context.Context) error { return errors.New("transient ping failure") }

func (r *countingRegistrar) Register(context.Context, *registry.ServiceInstance) error {
	r.registered.Add(1)
	return nil
}

func (*countingRegistrar) Deregister(context.Context, *registry.ServiceInstance) error { return nil }
