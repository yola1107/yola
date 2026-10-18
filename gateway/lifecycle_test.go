package gateway

import (
	"context"
	"errors"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yola/api/protocol/v1"
	"yola/internal/contextwait"
	"yola/internal/gateclient"
	"yola/locate"
	"yola/network/tcp"

	"github.com/go-kratos/kratos/v3"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
)

func TestStopTimeoutKeepsOneCleanupForLaterCallers(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	started := make(chan struct{})
	stopping := make(chan struct{})
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	var stops atomic.Int32
	gate := newTestServer(t, Listener(listener), Transport(&stubTransport{
		start: func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			return nil
		},
		stop: func(context.Context) error {
			stops.Add(1)
			close(stopping)
			<-release
			return nil
		},
	}))
	done := make(chan error, 1)
	ctx := kratos.NewContext(context.Background(), testAppInfo{id: "gate-a", name: "gateway"})
	go func() { done <- gate.Start(ctx) }()
	t.Cleanup(func() {
		unblock()
		require.NoError(t, gate.Stop(context.Background()))
	})
	receiveWithin(t, started)
	stopCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, gate.Stop(stopCtx), context.DeadlineExceeded)
	receiveWithin(t, stopping)
	stopped := make(chan error, 1)
	go func() { stopped <- gate.Stop(context.Background()) }()
	select {
	case stopErr := <-stopped:
		t.Fatalf("later Stop returned before cleanup: %v", stopErr)
	default:
	}
	require.Error(t, gate.Open(t.Context(), newTestConnection("conn-stopping")))
	unblock()
	require.NoError(t, receiveWithin(t, stopped))
	require.NoError(t, receiveWithin(t, done))
	require.NoError(t, gate.Stop(context.Background()))
	require.Equal(t, int32(1), stops.Load())
	rebound, err := net.Listen("tcp", address)
	require.NoError(t, err)
	require.NoError(t, rebound.Close())
}

func TestShutdownTimeoutClosesNetworkBeforeStalledSessionFinishes(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	ready := make(chan struct{})
	deadline := make(chan struct{})
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	conn := newTestConnection("conn-stalled-shutdown")
	conn.closeWithProto = func(ctx context.Context, _ *v1.Proto) error {
		<-ctx.Done()
		close(deadline)
		<-release
		return ctx.Err()
	}
	gate := newTestServer(
		t,
		Listener(listener),
		ShutdownTimeout(20*time.Millisecond),
		CleanupTimeout(200*time.Millisecond),
		Transport(&stubTransport{
			start: func(context.Context) error {
				close(ready)
				return nil
			},
		}),
	)
	started := make(chan error, 1)
	appCtx := kratos.NewContext(context.Background(), testAppInfo{id: "gate-a", name: "gateway"})
	go func() { started <- gate.Start(appCtx) }()
	t.Cleanup(func() {
		unblock()
		_ = gate.Stop(context.Background())
	})
	receiveWithin(t, ready)
	require.NoError(t, gate.Open(t.Context(), conn))
	callerCtx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	require.ErrorIs(t, gate.Stop(callerCtx), context.DeadlineExceeded)
	select {
	case <-deadline:
	case <-time.After(150 * time.Millisecond):
		t.Fatal("session drain did not use the shutdown budget")
	}
	receiveWithin(t, conn.closed)
	require.Eventually(t, func() bool {
		return tcpAddressReusable(address)
	}, time.Second, time.Millisecond)
	stopped := make(chan error, 1)
	go func() { stopped <- gate.Stop(context.Background()) }()
	select {
	case err := <-stopped:
		t.Fatalf("Stop returned before late session cleanup: %v", err)
	default:
	}
	unblock()
	require.ErrorIs(t, receiveWithin(t, stopped), context.DeadlineExceeded)
	require.ErrorIs(t, receiveWithin(t, started), context.DeadlineExceeded)
}

func TestShutdownTimeoutWaitsForAdmittedRemoteTakeover(t *testing.T) {
	store := testLocator(t)
	previousGate := newTestServer(t, Address("127.0.0.1:0"), Locator(store))
	initTestGateway(t, previousGate)
	previous := testBinding()
	previous.GateID, previous.GateEndpoint = previousGate.identity.id, previousGate.identity.endpoint
	previous.ConnID = "conn-previous"
	previousConn := newTestConnection(previous.ConnID)
	kicking := make(chan struct{})
	releaseKick := make(chan struct{})
	finishKick := sync.OnceFunc(func() { close(releaseKick) })
	defer finishKick()
	previousConn.closeWithProto = func(_ context.Context, msg *v1.Proto) error {
		if msg.Code != v1.KickCodeSessionReplaced {
			return errors.New("unexpected Kick code")
		}
		close(kicking)
		<-releaseKick
		return previousConn.Close()
	}
	require.NoError(t, previousGate.Open(t.Context(), previousConn))
	lease, _, err := store.BindGate(t.Context(), previous, time.Minute)
	require.NoError(t, err)
	require.True(t, previousGate.sessions.get(previous.ConnID).finishAuthentication(lease, time.Now()))

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	ready, stopping := make(chan struct{}), make(chan struct{})
	releaseStop := make(chan struct{})
	finishStop := sync.OnceFunc(func() { close(releaseStop) })
	gate := newTestServer(
		t,
		Listener(listener),
		Locator(store),
		ShutdownTimeout(20*time.Millisecond),
		CleanupTimeout(time.Second),
		Transport(&stubTransport{
			start: func(ctx context.Context) error {
				close(ready)
				<-ctx.Done()
				return nil
			},
			stop: func(context.Context) error {
				close(stopping)
				<-releaseStop
				return nil
			},
		}),
	)
	started, startDone := make(chan error, 1), make(chan struct{})
	appCtx := kratos.NewContext(context.Background(), testAppInfo{id: "gate-b", name: "gateway"})
	go func() {
		defer close(startDone)
		started <- gate.Start(appCtx)
	}()
	t.Cleanup(func() {
		finishKick()
		finishStop()
		_ = gate.Stop(context.Background())
		receiveWithin(t, startDone)
	})
	receiveWithin(t, ready)
	conn := newTestConnection("conn-current")
	require.NoError(t, gate.Open(t.Context(), conn))
	auth := authMessage(t)
	handled := make(chan error, 1)
	go func() {
		_, handleErr := gate.Handle(context.Background(), conn, auth)
		handled <- handleErr
	}()
	receiveWithin(t, kicking)
	current, err := store.LocateGate(t.Context(), previous.ServiceName, previous.UID)
	require.NoError(t, err)
	require.Equal(t, "gate-b", current.Binding.GateID)

	callerCtx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	require.ErrorIs(t, gate.Stop(callerCtx), context.DeadlineExceeded)
	receiveWithin(t, stopping)
	require.Eventually(t, func() bool {
		return tcpAddressReusable(listener.Addr().String())
	}, time.Second, time.Millisecond, "远端接管与迟到 Stop 不得阻止内部 listener 关闭")
	stopped := make(chan error, 1)
	go func() { stopped <- gate.Stop(context.Background()) }()

	finishKick()
	require.NoError(t, receiveWithin(t, handled))
	require.Zero(t, auth.Code)
	require.True(t, isClosed(previousConn.closed)())
	require.Eventually(t, func() bool {
		return errors.Is(gate.gateways.Kick(t.Context(), previous, v1.KickCodeSessionReplaced), gateclient.ErrUnavailable)
	}, time.Second, time.Millisecond, "接管结束后由 shutdown 关闭回程连接池，再汇合迟到 Stop")
	select {
	case stopErr := <-stopped:
		t.Fatalf("Stop returned before the admitted transport cleanup finished: %v", stopErr)
	default:
	}
	finishStop()
	require.ErrorIs(t, receiveWithin(t, stopped), context.DeadlineExceeded)
	require.ErrorIs(t, receiveWithin(t, started), context.DeadlineExceeded)
	require.ErrorIs(t, gate.Stop(context.Background()), context.DeadlineExceeded)
}

func TestStopWaitsForPreparationRollback(t *testing.T) {
	prepareStarted := make(chan struct{})
	releasePrepare := make(chan struct{})
	rollbackStarted := make(chan struct{})
	releaseRollback := make(chan struct{})
	releasePreparation := sync.OnceFunc(func() { close(releasePrepare) })
	releaseCleanup := sync.OnceFunc(func() { close(releaseRollback) })
	var stopCalls atomic.Int32
	gate := newTestServer(t, Transport(&stubTransport{
		prepare: func(context.Context) error {
			close(prepareStarted)
			<-releasePrepare
			return nil
		},
		stop: func(context.Context) error {
			stopCalls.Add(1)
			close(rollbackStarted)
			<-releaseRollback
			return nil
		},
	}))
	t.Cleanup(func() {
		releasePreparation()
		releaseCleanup()
		_ = gate.Stop(context.Background())
	})
	ctx := kratos.NewContext(context.Background(), testAppInfo{id: "gate-a", name: "gateway"})
	started := make(chan error, 1)
	go func() { started <- gate.Start(ctx) }()
	receiveWithin(t, prepareStarted)
	stopCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, gate.Stop(stopCtx), context.DeadlineExceeded)
	releasePreparation()
	receiveWithin(t, rollbackStarted)
	stopped := make(chan error, 1)
	go func() { stopped <- gate.Stop(context.Background()) }()
	select {
	case err := <-stopped:
		t.Fatalf("Stop returned before rollback completed: %v", err)
	default:
	}
	releaseCleanup()
	require.ErrorIs(t, receiveWithin(t, started), context.Canceled)
	require.NoError(t, receiveWithin(t, stopped))
	require.NoError(t, gate.Stop(context.Background()))
	require.Equal(t, int32(1), stopCalls.Load())
}

func TestPreparationTimeoutClosesAlreadyPreparedClientListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	preparing := make(chan struct{})
	release := make(chan struct{})
	resume := sync.OnceFunc(func() { close(release) })
	var stops atomic.Int32
	gate := newTestServer(t, ShutdownTimeout(20*time.Millisecond), Transport(
		tcp.NewServer(tcp.Listener(listener)),
		&stubTransport{
			prepare: func(context.Context) error {
				close(preparing)
				<-release
				return nil
			},
			stop: func(context.Context) error {
				stops.Add(1)
				return nil
			},
		},
	))
	started := make(chan error, 1)
	go func() {
		started <- gate.Start(kratos.NewContext(context.Background(), testAppInfo{id: "gate-a", name: "gateway"}))
	}()
	t.Cleanup(func() {
		resume()
		_ = gate.Stop(context.Background())
	})
	receiveWithin(t, preparing)
	stopped := make(chan error, 1)
	go func() { stopped <- gate.Stop(context.Background()) }()
	require.Eventually(t, func() bool {
		return tcpAddressReusable(address)
	}, time.Second, time.Millisecond, "prepared client listener must close before late preparation returns")
	require.Zero(t, stops.Load(), "the in-flight transport still belongs to its creator")
	resume()
	require.ErrorIs(t, receiveWithin(t, stopped), context.DeadlineExceeded)
	require.Error(t, receiveWithin(t, started))
	require.Equal(t, int32(1), stops.Load())
}

func TestShutdownTimeoutClosesNetworkBeforeLateTransportStop(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	ready := make(chan struct{})
	expired := make(chan struct{})
	release := make(chan struct{})
	resume := sync.OnceFunc(func() { close(release) })
	gate := newTestServer(t, Listener(listener), ShutdownTimeout(20*time.Millisecond), Transport(&stubTransport{
		start: func(context.Context) error {
			close(ready)
			return nil
		},
		stop: func(ctx context.Context) error {
			<-ctx.Done()
			close(expired)
			<-release
			return ctx.Err()
		},
	}))
	started := make(chan error, 1)
	go func() {
		started <- gate.Start(kratos.NewContext(context.Background(), testAppInfo{id: "gate-a", name: "gateway"}))
	}()
	t.Cleanup(func() {
		resume()
		_ = gate.Stop(context.Background())
	})
	receiveWithin(t, ready)
	stopped := make(chan error, 1)
	go func() { stopped <- gate.Stop(context.Background()) }()
	receiveWithin(t, expired)
	require.Eventually(t, func() bool {
		return tcpAddressReusable(address)
	}, time.Second, time.Millisecond)
	resume()
	require.ErrorIs(t, receiveWithin(t, stopped), context.DeadlineExceeded)
	require.ErrorIs(t, receiveWithin(t, started), context.DeadlineExceeded)
}

func TestGatewayCleansUpAfterTransportFailure(t *testing.T) {
	transportErr := errors.New("transport failed")
	grpcLis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := grpcLis.Addr().String()
	gate := newTestGateway(t, testLocator(t), "grpc://127.0.0.1:1", Listener(grpcLis),
		Transport(tcp.NewServer(tcp.Listener(&failingListener{acceptErr: transportErr}))))
	require.ErrorIs(t, newTestApp(gate).Run(), transportErr)
	require.Error(t, gate.Open(t.Context(), newTestConnection("conn-after-failure")))
	rebound, err := net.Listen("tcp", address)
	require.NoError(t, err)
	require.NoError(t, rebound.Close())
	require.NoError(t, gate.Stop(context.Background()))
}

func TestStopLabelsEachClientTransportFailure(t *testing.T) {
	firstErr := errors.New("first stop failed")
	secondErr := errors.New("second stop failed")
	ready := make(chan struct{})
	gate := newTestServer(t, Transport(
		&stubTransport{stop: func(context.Context) error { return firstErr }},
		&stubTransport{
			start: func(context.Context) error {
				close(ready)
				return nil
			},
			stop: func(context.Context) error { return secondErr },
		},
	))
	started := make(chan error, 1)
	ctx := kratos.NewContext(context.Background(), testAppInfo{id: "gate-a", name: "gateway"})
	go func() { started <- gate.Start(ctx) }()
	receiveWithin(t, ready)
	err := gate.Stop(context.Background())
	require.ErrorIs(t, err, firstErr)
	require.ErrorIs(t, err, secondErr)
	require.ErrorContains(t, err, "gateway: stop client transport 0 (*gateway.stubTransport)")
	require.ErrorContains(t, err, "gateway: stop client transport 1 (*gateway.stubTransport)")
	require.ErrorIs(t, receiveWithin(t, started), firstErr)
}

func TestDrainSessionsSendsShutdownKick(t *testing.T) {
	conn := newTestConnection("conn-shutdown")
	gate := &Server{
		sessions: &sessionRegistry{byConnID: map[string]*session{
			conn.ConnID(): {conn: conn},
		}},
		cleanupTimeout: time.Second,
	}

	receiveWithin(t, gate.startSessionDrain(context.Background(), gate.sessions.takeAll()))

	require.True(t, isClosed(conn.closed)())
	require.Nil(t, gate.sessions.byConnID)
	require.Nil(t, gate.sessions.get(conn.ConnID()))
	require.Error(t, gate.sessions.add(newTestConnection("conn-late"), time.Second))
	select {
	case msg := <-conn.pushes:
		require.Equal(t, v1.OpKick, msg.Op)
		require.Equal(t, v1.KickCodeServerShutdown, msg.Code)
	case <-time.After(time.Second):
		t.Fatal("expected shutdown OpKick")
	}
}

func TestShutdownTimeoutCleansLateAuthentication(t *testing.T) {
	store := newBlockingBindLocator(testLocator(t))
	ready, stopping := make(chan struct{}), make(chan struct{})
	gate := newTestServer(t, Locator(store), ShutdownTimeout(20*time.Millisecond), Transport(&stubTransport{
		start: func(ctx context.Context) error {
			close(ready)
			<-ctx.Done()
			return nil
		},
		stop: func(context.Context) error {
			close(stopping)
			return nil
		},
	}))
	started := make(chan error, 1)
	go func() {
		started <- gate.Start(kratos.NewContext(context.Background(), testAppInfo{id: "gate-a", name: "gateway"}))
	}()
	t.Cleanup(func() {
		store.unblock()
		_ = gate.Stop(context.Background())
	})
	receiveWithin(t, ready)
	conn := newTestConnection("conn-late-authentication")
	require.NoError(t, gate.Open(t.Context(), conn))
	auth := authMessage(t)
	handled := make(chan error, 1)
	go func() {
		_, err := gate.Handle(context.Background(), conn, auth)
		handled <- err
	}()
	candidate := receiveWithin(t, store.entered)
	stopped := make(chan error, 1)
	go func() { stopped <- gate.Stop(context.Background()) }()

	receiveWithin(t, stopping)
	receiveWithin(t, conn.closed)
	require.Error(t, gate.Open(t.Context(), newTestConnection("conn-after-stop")))
	select {
	case err := <-stopped:
		t.Fatalf("Stop returned before late authentication cleanup: %v", err)
	default:
	}
	store.unblock()

	require.NoError(t, receiveWithin(t, handled))
	require.Equal(t, int32(codes.Aborted), auth.Code)
	_, err := store.LocateGate(t.Context(), candidate.ServiceName, candidate.UID)
	require.ErrorIs(t, err, locate.ErrGateNotFound)
	require.ErrorIs(t, receiveWithin(t, stopped), context.DeadlineExceeded)
	require.ErrorIs(t, receiveWithin(t, started), context.DeadlineExceeded)
}

func TestOpenConcurrentWithStopRetainsAcceptedConnectionForDrain(t *testing.T) {
	ready, canceling := make(chan struct{}), make(chan struct{})
	gate := newTestServer(t, Transport(&stubTransport{
		start: func(ctx context.Context) error {
			close(ready)
			<-ctx.Done()
			close(canceling)
			return nil
		},
	}))
	started := make(chan error, 1)
	go func() {
		started <- gate.Start(kratos.NewContext(context.Background(), testAppInfo{id: "gate-a", name: "gateway"}))
	}()
	conn := &blockingOpenConnection{
		testConnection: newTestConnection("conn-opening"),
		entered:        make(chan struct{}),
		release:        make(chan struct{}),
	}
	unblock := sync.OnceFunc(func() { close(conn.release) })
	t.Cleanup(func() {
		unblock()
		_ = gate.Stop(context.Background())
	})
	receiveWithin(t, ready)
	opened := make(chan error, 1)
	go func() { opened <- gate.Open(t.Context(), conn) }()
	receiveWithin(t, conn.entered)
	stopped := make(chan error, 1)
	go func() { stopped <- gate.Stop(context.Background()) }()
	// Start 的取消证明 Stop 已开始；Open 仍持有登记和准入共用的锁。
	receiveWithin(t, canceling)
	unblock()

	require.NoError(t, receiveWithin(t, opened))
	require.NoError(t, receiveWithin(t, stopped))
	require.NoError(t, receiveWithin(t, started))
	require.True(t, isClosed(conn.closed)())
	require.Equal(t, v1.KickCodeServerShutdown, receiveWithin(t, conn.pushes).Code)
	require.Nil(t, gate.sessions.get(conn.connID))
	require.Error(t, gate.Open(t.Context(), newTestConnection("conn-after-stop")))
}

func TestDrainSessionsRedistributesWorkFromBlockedWorker(t *testing.T) {
	const sessionCount = _shutdownWorkerCount * 2
	blocked := make(chan struct{})
	release := make(chan struct{})
	releaseConnections := sync.OnceFunc(func() { close(release) })
	var claimedBlocker atomic.Bool
	var completed atomic.Int32
	registry := &sessionRegistry{byConnID: make(map[string]*session, sessionCount)}
	for index := range sessionCount {
		connID := "conn-" + strconv.Itoa(index)
		conn := newTestConnection(connID)
		conn.closeWithProto = func(context.Context, *v1.Proto) error {
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
	gate := &Server{sessions: registry, cleanupTimeout: time.Second}
	drained := gate.startSessionDrain(context.Background(), gate.sessions.takeAll())
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
}

func TestDrainSessionsPreservesShutdownDeadlineForCleanup(t *testing.T) {
	binding := testBinding()
	conn := newTestConnection(binding.ConnID)
	store := &blockingUnbindLocator{Locator: testLocator(t), called: make(chan context.Context, 1)}
	gate := &Server{
		locator:        store,
		backends:       newBackends(staticDiscovery{}, nil, 300*time.Millisecond, store),
		sessions:       &sessionRegistry{byConnID: map[string]*session{binding.ConnID: activeSession(conn, binding)}},
		cleanupTimeout: 300 * time.Millisecond,
	}
	t.Cleanup(gate.backends.close)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	_ = contextwait.Done(ctx, gate.startSessionDrain(ctx, gate.sessions.takeAll()))
	require.Less(t, time.Since(started), 150*time.Millisecond)
	cleanupCtx := receiveWithin(t, store.called)
	deadline, ok := cleanupCtx.Deadline()
	require.True(t, ok)
	require.LessOrEqual(t, deadline.Sub(started), 50*time.Millisecond)
}

func TestSessionDetachForClose(t *testing.T) {
	binding := testBinding()
	tests := []struct {
		name    string
		binding locate.GateBinding
	}{
		{name: "authenticated", binding: binding},
		{name: "unauthenticated"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sess := &session{
				conn:          newTestConnection(binding.ConnID),
				binding:       test.binding,
				leaseDeadline: time.Now().Add(time.Minute),
			}

			require.Equal(t, test.binding, sess.detachForClose())
			require.Empty(t, sess.binding)
			require.True(t, sess.leaseDeadline.IsZero())
		})
	}
}

func TestRPCTimeoutDoesNotCancelCleanup(t *testing.T) {
	gate := newTestServer(t, RPCTimeout(time.Nanosecond))
	t.Cleanup(func() { require.NoError(t, gate.Stop(context.Background())) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cleanupCtx, cancelCleanup := gate.cleanupContext(ctx)
	defer cancelCleanup()
	require.NoError(t, cleanupCtx.Err())
	deadline, ok := cleanupCtx.Deadline()
	require.True(t, ok)
	require.Greater(t, time.Until(deadline), time.Second)
}

type blockingOpenConnection struct {
	*testConnection
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *blockingOpenConnection) ConnID() string {
	c.once.Do(func() {
		close(c.entered)
		<-c.release
	})
	return c.testConnection.ConnID()
}
