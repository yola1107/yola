package gateway

import (
	"context"
	"errors"
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

func TestLocateStatusCode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want codes.Code
	}{
		{name: "gate missing", err: locate.ErrGateNotFound, want: codes.Aborted},
		{name: "gate conflict", err: locate.ErrGateConflict, want: codes.Aborted},
		{name: "canceled", err: context.Canceled, want: codes.Canceled},
		{name: "deadline", err: context.DeadlineExceeded, want: codes.DeadlineExceeded},
		{name: "invalid binding", err: locate.ErrInvalidGateBinding, want: codes.Internal},
		{name: "invalid lease", err: locate.ErrInvalidGateLease, want: codes.Internal},
		{name: "invalid ttl", err: locate.ErrInvalidGateTTL, want: codes.Internal},
		{name: "redis failure", err: errors.New("connection refused"), want: codes.Unavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, locateStatusCode(tc.err))
		})
	}
}

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

func TestHeartbeatClassifiesLocatorFailures(t *testing.T) {
	for _, test := range []struct {
		name       string
		err        error
		wantCode   codes.Code
		wantClosed bool
	}{
		{name: "unavailable", err: errors.New("redis unavailable")},
		{name: "deadline", err: context.DeadlineExceeded},
		{name: "canceled", err: context.Canceled, wantCode: codes.Canceled},
		{name: "missing", err: locate.ErrGateNotFound, wantCode: codes.Aborted, wantClosed: true},
		{name: "binding changed", err: locate.ErrGateConflict, wantCode: codes.Aborted, wantClosed: true},
		{name: "invalid lease", err: locate.ErrInvalidGateLease, wantCode: codes.Internal, wantClosed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &renewLeaseLocator{Locator: testLocator(t), err: test.err}
			gateway := &Server{locator: store, rpcTimeout: time.Second, leaseTTL: time.Minute}
			conn := newTestConnection("conn-a")
			sess := activeSession(conn, testBinding())
			sess.leaseDeadline = time.Now().Add(gateway.leaseTTL / 2)

			err := gateway.heartbeat(context.Background(), sess)

			require.Equal(t, test.wantCode, status.Code(err))
			require.Equal(t, test.wantClosed, isClosed(conn.closed)())
			require.Equal(t, int32(1), store.calls.Load())
		})
	}
}

func TestHeartbeatRenewsAtHalfTTLAndRetriesTransientFailure(t *testing.T) {
	store := &renewLeaseLocator{Locator: testLocator(t), errors: []error{context.DeadlineExceeded, nil}}
	gateway := &Server{locator: store, rpcTimeout: time.Second, leaseTTL: time.Minute}
	sess := activeSession(newTestConnection("conn-a"), testBinding())
	sess.leaseDeadline = time.Now().Add(gateway.leaseTTL / 2)
	initialDeadline := sess.leaseDeadline

	require.NoError(t, gateway.heartbeat(context.Background(), sess))
	require.Equal(t, initialDeadline, sess.leaseDeadline)
	require.NoError(t, gateway.heartbeat(context.Background(), sess))
	require.Greater(t, sess.leaseDeadline, initialDeadline)
	require.NoError(t, gateway.heartbeat(context.Background(), sess))
	require.Equal(t, int32(2), store.calls.Load())
}

type renewLeaseLocator struct {
	locate.Locator
	err    error
	errors []error
	calls  atomic.Int32
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

func (s *renewLeaseLocator) RenewGateLease(_ context.Context, binding locate.GateBinding, ttl time.Duration) (locate.GateLease, error) {
	call := int(s.calls.Add(1)) - 1
	err := s.err
	if call < len(s.errors) {
		err = s.errors[call]
	}
	if err != nil {
		return locate.GateLease{}, err
	}
	return locate.GateLease{Binding: binding, TTL: ttl}, nil
}
