package gateway

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	protocolv1 "yola/api/protocol/v1"
	"yola/locate"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestAuthenticationCommitsSuccessfulRoute(t *testing.T) {
	store := testLocator(t)
	var calls atomic.Int32
	deadlines := make(chan bool, 1)
	services := make(chan string, 1)
	gateway := newTestServer(t,
		Auth(testAuthenticator{calls: &calls, deadlines: deadlines, services: services}),
		Locator(store),
		Discovery(staticDiscovery{}),
		LeaseTTL(time.Minute),
	)
	initTestGateway(t, gateway)
	conn := newTestConnection("conn-a")
	require.NoError(t, gateway.Open(context.Background(), conn))
	auth := authMessage(t)

	_, err := gateway.Handle(context.Background(), conn, auth)

	require.NoError(t, err)
	require.Equal(t, int32(1), calls.Load())
	require.True(t, <-deadlines)
	require.Equal(t, "game", <-services)
	require.Equal(t, protocolv1.OpAuthReply, auth.Op)
	require.Equal(t, int32(codes.OK), auth.Code)
	binding, valid := gateway.sessions.get(conn.ConnID()).route(time.Now())
	require.True(t, valid)
	require.Equal(t, "game", binding.ServiceName)
	require.Equal(t, "player-a", binding.UID)
	require.Equal(t, conn.ConnID(), binding.ConnID)
}

func TestAuthenticationDoesNotDependOnNodeAvailability(t *testing.T) {
	store := testLocator(t)
	gateway := newTestServer(t,
		Auth(testAuthenticator{}),
		Locator(store),
		Discovery(staticDiscovery{}),
		LeaseTTL(time.Minute),
	)
	initTestGateway(t, gateway)
	conn := newTestConnection("conn-a")
	require.NoError(t, gateway.Open(context.Background(), conn))

	auth := authMessage(t)
	_, err := gateway.Handle(context.Background(), conn, auth)
	require.NoError(t, err)
	require.Equal(t, int32(codes.OK), auth.Code)
	_, err = store.LocateGate(context.Background(), "game", "player-a")
	require.NoError(t, err)

	request := &protocolv1.Proto{Op: protocolv1.OpRequest, Cmd: 1}
	_, err = gateway.Handle(context.Background(), conn, request)
	require.NoError(t, err)
	require.Equal(t, int32(codes.Unavailable), request.Code)
	require.False(t, isClosed(conn.closed)())
}

func TestAuthenticationRejectsExpiredGateLease(t *testing.T) {
	baseStore := testLocator(t)
	store := &delayedBindLocator{Locator: baseStore, delay: 100 * time.Millisecond}
	gateway := newTestServer(t,
		Auth(testAuthenticator{}),
		Locator(store),
		Discovery(staticDiscovery{}),
		LeaseTTL(50*time.Millisecond),
	)
	initTestGateway(t, gateway)
	previous := testBinding()
	previous.ConnID = "conn-old"
	previous.BindingToken = "binding-old"
	_, _, err := baseStore.BindGate(context.Background(), previous, time.Minute)
	require.NoError(t, err)
	oldConn := newTestConnection(previous.ConnID)
	require.NoError(t, gateway.Open(context.Background(), oldConn))
	require.True(t, gateway.sessions.get(previous.ConnID).finishAuthentication(
		locate.GateLease{Binding: previous, TTL: time.Minute}, time.Now(),
	))
	conn := newTestConnection("conn-a")
	require.NoError(t, gateway.Open(context.Background(), conn))

	auth := authMessage(t)
	_, err = gateway.Handle(context.Background(), conn, auth)
	require.NoError(t, err)
	require.Equal(t, int32(codes.Aborted), auth.Code)
	binding, valid := gateway.sessions.get(conn.ConnID()).route(time.Now())
	require.False(t, valid)
	require.False(t, locate.ValidGateBinding(binding))
	require.False(t, isClosed(oldConn.closed)())
}

func TestAuthenticationUsesDeadlineFromOpen(t *testing.T) {
	store := &bindCountingLocator{Locator: testLocator(t)}
	deadlines := make(chan time.Time, 1)
	const authTimeout = 200 * time.Millisecond
	gateway := newTestServer(t,
		Auth(authenticatorFunc(func(ctx context.Context, _ string, _ []byte, _ string) (string, error) {
			deadline, ok := ctx.Deadline()
			require.True(t, ok)
			deadlines <- deadline
			<-ctx.Done()
			return "", ctx.Err()
		})),
		Locator(store),
		Discovery(staticDiscovery{}),
		AuthTimeout(authTimeout),
		LeaseTTL(time.Minute),
	)
	initTestGateway(t, gateway)
	conn := newTestConnection("conn-a")
	openedBefore := time.Now()
	require.NoError(t, gateway.Open(context.Background(), conn))
	openedAfter := time.Now()
	time.Sleep(120 * time.Millisecond)

	auth := authMessage(t)
	_, err := gateway.Handle(context.Background(), conn, auth)
	require.NoError(t, err)

	deadline := receiveWithin(t, deadlines)
	require.False(t, deadline.Before(openedBefore.Add(authTimeout)))
	require.False(t, deadline.After(openedAfter.Add(authTimeout)))
	require.Equal(t, int32(codes.DeadlineExceeded), auth.Code)
	require.Zero(t, store.binds.Load())
}

func TestAuthenticationRejectsInvalidUID(t *testing.T) {
	gateway := newTestServer(t,
		Auth(authenticatorFunc(func(context.Context, string, []byte, string) (string, error) {
			return strings.Repeat("a", 129), nil
		})),
	)

	_, err := gateway.authenticateUID(context.Background(), "game", nil, "conn-a")
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}

func TestAuthenticationDoesNotBindAfterAdmissionCloses(t *testing.T) {
	store := &bindCountingLocator{Locator: testLocator(t)}
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	gateway := newTestServer(t,
		Auth(authenticatorFunc(func(context.Context, string, []byte, string) (string, error) {
			close(entered)
			<-release
			return "player-a", nil
		})),
		Locator(store),
		Discovery(staticDiscovery{}),
	)
	initTestGateway(t, gateway)
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	conn := newTestConnection("conn-a")
	require.NoError(t, gateway.Open(context.Background(), conn))
	auth := authMessage(t)
	handled := make(chan error, 1)
	go func() {
		_, err := gateway.Handle(context.Background(), conn, auth)
		handled <- err
	}()
	receiveWithin(t, entered)

	require.NoError(t, gateway.quiesce(context.Background()))
	releaseOnce.Do(func() { close(release) })
	require.NoError(t, receiveWithin(t, handled))
	require.NotEqual(t, int32(codes.OK), auth.Code)
	require.Zero(t, store.binds.Load())
}

func TestAuthenticationCleansCandidateAfterConnectionCloses(t *testing.T) {
	store := newBlockingBindLocator(testLocator(t))
	gateway := newTestServer(t, Locator(store), Discovery(staticDiscovery{}))
	initTestGateway(t, gateway)
	t.Cleanup(store.unblock)
	conn := newTestConnection("conn-new")
	require.NoError(t, gateway.Open(context.Background(), conn))
	handleCtx, cancel := context.WithCancel(context.Background())
	auth := authMessage(t)
	handled := make(chan error, 1)
	go func() {
		_, err := gateway.Handle(handleCtx, conn, auth)
		handled <- err
	}()
	candidate := receiveWithin(t, store.entered)
	cancel()
	store.unblock()

	require.NoError(t, receiveWithin(t, handled))
	require.NotEqual(t, int32(codes.OK), auth.Code)
	_, err := store.LocateGate(context.Background(), candidate.ServiceName, candidate.UID)
	require.ErrorIs(t, err, locate.ErrGateNotFound)
}

func TestAuthenticationCleanupDoesNotUnbindThirdBinding(t *testing.T) {
	baseStore := testLocator(t)
	store := newBlockingBindLocator(baseStore)
	gateway := newTestServer(t,
		Auth(testAuthenticator{}),
		Locator(store),
		Discovery(staticDiscovery{}),
		LeaseTTL(time.Minute),
	)
	initTestGateway(t, gateway)
	t.Cleanup(store.unblock)
	conn := newTestConnection("conn-new")
	require.NoError(t, gateway.Open(context.Background(), conn))
	handleCtx, cancel := context.WithCancel(context.Background())
	auth := authMessage(t)
	handled := make(chan error, 1)
	go func() {
		_, err := gateway.Handle(handleCtx, conn, auth)
		handled <- err
	}()
	candidate := receiveWithin(t, store.entered)
	third := candidate
	third.ConnID = "conn-third"
	third.BindingToken = "binding-third"
	_, _, err := baseStore.BindGate(context.Background(), third, time.Minute)
	require.NoError(t, err)
	cancel()
	store.unblock()

	require.NoError(t, receiveWithin(t, handled))
	require.NotEqual(t, int32(codes.OK), auth.Code)
	current, err := baseStore.LocateGate(context.Background(), third.ServiceName, third.UID)
	require.NoError(t, err)
	require.Equal(t, third, current.Binding)
}

type delayedBindLocator struct {
	locate.Locator
	delay time.Duration
}

type authenticatorFunc func(context.Context, string, []byte, string) (string, error)

func (f authenticatorFunc) Authenticate(ctx context.Context, serviceName string, token []byte, remoteIP string) (string, error) {
	return f(ctx, serviceName, token, remoteIP)
}

type bindCountingLocator struct {
	locate.Locator
	binds atomic.Int32
}

func (s *bindCountingLocator) BindGate(ctx context.Context, binding locate.GateBinding, ttl time.Duration) (locate.GateLease, *locate.GateBinding, error) {
	s.binds.Add(1)
	return s.Locator.BindGate(ctx, binding, ttl)
}

func (s *delayedBindLocator) BindGate(ctx context.Context, binding locate.GateBinding, ttl time.Duration) (locate.GateLease, *locate.GateBinding, error) {
	lease, previous, err := s.Locator.BindGate(ctx, binding, ttl)
	time.Sleep(s.delay)
	return lease, previous, err
}

func TestAuthenticationBindAdmittedBeforeShutdownCommitsTakeover(t *testing.T) {
	baseStore := testLocator(t)
	store := newBlockingBindLocator(baseStore)
	gateway := newTestServer(t,
		Auth(testAuthenticator{}),
		Locator(store),
		Discovery(staticDiscovery{}),
		LeaseTTL(time.Minute),
	)
	initTestGateway(t, gateway)
	t.Cleanup(store.unblock)
	previous := testBinding()
	previous.GateID = gateway.identity.id
	previous.GateEndpoint = gateway.identity.endpoint
	previous.ConnID = "conn-old"
	previous.BindingToken = "binding-old"
	_, _, err := baseStore.BindGate(context.Background(), previous, time.Minute)
	require.NoError(t, err)
	oldConn := newTestConnection(previous.ConnID)
	require.NoError(t, gateway.Open(context.Background(), oldConn))
	require.True(t, gateway.sessions.get(previous.ConnID).finishAuthentication(
		locate.GateLease{Binding: previous, TTL: time.Minute}, time.Now(),
	))

	conn := newTestConnection("conn-new")
	require.NoError(t, gateway.Open(context.Background(), conn))
	auth := authMessage(t)
	handled := make(chan error, 1)
	go func() {
		_, err := gateway.Handle(context.Background(), conn, auth)
		handled <- err
	}()
	receiveWithin(t, store.entered)
	stopped := make(chan error, 1)
	go func() { stopped <- gateway.quiesce(context.Background()) }()
	select {
	case err := <-stopped:
		store.unblock()
		require.FailNow(t, "quiesce returned while an admitted BindGate was blocked", "error: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	store.unblock()

	require.NoError(t, receiveWithin(t, handled))
	require.NoError(t, receiveWithin(t, stopped))
	require.Equal(t, int32(codes.OK), auth.Code)
	require.Eventually(t, isClosed(oldConn.closed), time.Second, time.Millisecond)
}

func TestSessionReplacementKickDoesNotNotifyNodeDisconnect(t *testing.T) {
	gateway, store, conn, binding, disconnects := newDisconnectTrackingServer(t)
	bindTestPlayerNode(t, store, binding.ServiceName, binding.UID, "node-a")

	require.NoError(t, gateway.kick(context.Background(), binding, protocolv1.KickCodeSessionReplaced))
	require.True(t, isClosed(conn.closed)())
	select {
	case disconnect := <-disconnects:
		t.Fatalf("session replacement sent unexpected Disconnect: %+v", disconnect)
	default:
	}
	_, err := store.LocateGate(context.Background(), binding.ServiceName, binding.UID)
	require.ErrorIs(t, err, locate.ErrGateNotFound)
	nodeID, err := store.LocateNode(context.Background(), binding.ServiceName, binding.UID)
	require.NoError(t, err)
	require.Equal(t, "node-a", nodeID)
}

func TestTakeoverKickSurvivesCanceledAuthentication(t *testing.T) {
	conn := newTestConnection("conn-old")
	ctxErr := make(chan error, 1)
	conn.closeWithProto = func(ctx context.Context, _ *protocolv1.Proto) error {
		ctxErr <- ctx.Err()
		return conn.Close()
	}
	binding := testBinding()
	binding.ConnID = conn.ConnID()
	gateway := &Server{
		identity:       identity{id: binding.GateID, endpoint: binding.GateEndpoint},
		locator:        testLocator(t),
		sessions:       &sessionRegistry{byConnID: map[string]*session{binding.ConnID: activeSession(conn, binding)}},
		cleanupTimeout: time.Second,
	}
	authCtx, cancel := context.WithCancel(context.Background())
	cancel()
	gateway.kickPrevious(authCtx, binding)

	require.NoError(t, receiveWithin(t, ctxErr))
	require.True(t, isClosed(conn.closed)())
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
