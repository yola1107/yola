package gateway

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	v1 "yola/api/protocol/v1"
	"yola/locate"
	"yola/network"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRPCError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code codes.Code
	}{
		{name: "invalid push", err: errInvalidPush, code: codes.InvalidArgument},
		{name: "invalid kick", err: errInvalidKick, code: codes.InvalidArgument},
		{name: "missing", err: errConnectionMissing, code: codes.NotFound},
		{name: "binding changed", err: errBindingChanged, code: codes.Aborted},
		{name: "busy", err: errConnectionBusy, code: codes.ResourceExhausted},
		{name: "frame too large", err: network.ErrFrameTooLarge, code: codes.ResourceExhausted},
		{name: "closed", err: errConnectionClosed, code: codes.Unavailable},
		{name: "unknown", err: errors.New("unknown"), code: codes.Internal},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.code, status.Code(rpcError(test.err)))
		})
	}
}

func TestClusterServiceRejectsInvalidRequest(t *testing.T) {
	_, err := (&pushService{}).Push(context.Background(), nil)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = (&pushService{}).Kick(context.Background(), nil)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestServerPushMapsConnectionErrors(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want error
	}{
		{name: "queue full", err: network.ErrSendQueueFull, want: errConnectionBusy},
		{name: "frame too large", err: network.ErrFrameTooLarge, want: network.ErrFrameTooLarge},
		{name: "closed", err: network.ErrConnectionClosed, want: errConnectionClosed},
	} {
		t.Run(test.name, func(t *testing.T) {
			gateway, _, conn, current := newAuthenticatedServer(t)
			conn.sendErr = test.err
			err := gateway.push(current, &v1.Proto{Op: v1.OpPush})
			require.ErrorIs(t, err, test.want)
		})
	}
}

func TestServerPushRejectsOversizedFrameBeforeSend(t *testing.T) {
	gateway, _, conn, binding := newAuthenticatedServer(t)

	err := gateway.push(binding, &v1.Proto{
		Op:   v1.OpPush,
		Body: make([]byte, v1.MaxProtoSize),
	})

	require.ErrorIs(t, err, network.ErrFrameTooLarge)
	select {
	case <-conn.pushes:
		t.Fatal("oversized push entered the connection queue")
	default:
	}
}

func TestServerPushRejectsExpiredBinding(t *testing.T) {
	binding := testBinding()
	conn := newTestConnection(binding.ConnID)
	sess := activeSession(conn, binding)
	sess.leaseDeadline = time.Now().Add(-time.Millisecond)
	gateway := &Server{
		identity: identity{id: binding.GateID, endpoint: binding.GateEndpoint},
		sessions: &sessionRegistry{byConnID: map[string]*session{binding.ConnID: sess}},
	}

	err := gateway.push(binding, &v1.Proto{Op: v1.OpPush})

	require.ErrorIs(t, err, errBindingChanged)
	select {
	case <-conn.pushes:
		t.Fatal("expired push entered the connection queue")
	default:
	}
}

func TestServerKickRequiresCurrentBinding(t *testing.T) {
	gateway, _, conn, binding := newAuthenticatedServer(t)

	invalid := binding
	invalid.GateID = ""
	require.ErrorIs(t, gateway.kick(context.Background(), invalid, 1), errInvalidKick)

	stale := binding
	stale.BindingToken = "binding-stale"
	require.NoError(t, gateway.kick(context.Background(), stale, 1))
	require.False(t, isClosed(conn.closed)())

	require.NoError(t, gateway.kick(context.Background(), binding, 7))
	require.True(t, isClosed(conn.closed)())
	kick := <-conn.pushes
	require.Equal(t, v1.OpKick, kick.Op)
	require.Equal(t, int32(7), kick.Code)
}

func TestServerKickUnbindsGateWhenFinalCloseFails(t *testing.T) {
	gateway, store, conn, binding := newAuthenticatedServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	conn.closeWithProto = func(context.Context, *v1.Proto) error {
		return errors.New("close failed")
	}

	require.NoError(t, gateway.kick(ctx, binding, 7))
	require.True(t, isClosed(conn.closed)())
	_, err := store.LocateGate(context.Background(), binding.ServiceName, binding.UID)
	require.ErrorIs(t, err, locate.ErrGateNotFound)
}

func TestServerKickUsesSingleCleanupTimeout(t *testing.T) {
	output := captureGatewayLogs(t)
	store := &blockingUnbindLocator{Locator: testLocator(t), called: make(chan context.Context, 1)}
	gateway := newTestServer(t,
		Locator(store),
		CleanupTimeout(30*time.Millisecond),
		LeaseTTL(time.Minute),
	)
	initTestGateway(t, gateway)
	conn := newTestConnection("conn-a")
	require.NoError(t, gateway.Open(context.Background(), conn))
	_, err := gateway.Handle(context.Background(), conn, authMessage(t))
	require.NoError(t, err)
	lease, err := store.LocateGate(context.Background(), "game", "player-a")
	require.NoError(t, err)
	closeContexts := make(chan context.Context, 1)
	conn.closeWithProto = func(ctx context.Context, _ *v1.Proto) error {
		closeContexts <- ctx
		return nil
	}
	callerCtx, cancel := context.WithCancel(context.Background())
	cancel()

	require.NoError(t, gateway.kick(callerCtx, lease.Binding, 7))
	closeCtx := receiveWithin(t, closeContexts)
	unbindCtx := receiveWithin(t, store.called)
	closeDeadline, closeOK := closeCtx.Deadline()
	unbindDeadline, unbindOK := unbindCtx.Deadline()
	require.True(t, closeOK && unbindOK)
	require.Equal(t, closeDeadline, unbindDeadline)
	require.ErrorIs(t, unbindCtx.Err(), context.DeadlineExceeded)
	require.Contains(t, output.String(), `msg="unbind Gate failed"`)
}

func TestServerKickCleanupDoesNotUnbindThirdBinding(t *testing.T) {
	gateway, store, conn, binding := newAuthenticatedServer(t)
	closeStarted := make(chan struct{})
	finishClose := make(chan struct{})
	markCloseStarted := sync.OnceFunc(func() { close(closeStarted) })
	releaseClose := sync.OnceFunc(func() { close(finishClose) })
	t.Cleanup(releaseClose)
	conn.closeWithProto = func(_ context.Context, _ *v1.Proto) error {
		markCloseStarted()
		<-finishClose
		return nil
	}
	kicked := make(chan error, 1)
	go func() { kicked <- gateway.kick(context.Background(), binding, 7) }()
	receiveWithin(t, closeStarted)
	third := binding
	third.ConnID = "conn-third"
	third.BindingToken = "binding-third"
	_, _, err := store.BindGate(context.Background(), third, time.Minute)
	require.NoError(t, err)
	releaseClose()

	require.NoError(t, receiveWithin(t, kicked))
	current, err := store.LocateGate(context.Background(), third.ServiceName, third.UID)
	require.NoError(t, err)
	require.Equal(t, third, current.Binding)
}

func TestSessionReplacementKickDoesNotNotifyNodeDisconnect(t *testing.T) {
	gateway, store, conn, binding, disconnects := newDisconnectTrackingServer(t)
	bindTestPlayerNode(t, store, binding.ServiceName, binding.UID, "node-a")

	require.NoError(t, gateway.kick(context.Background(), binding, v1.KickCodeSessionReplaced))
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
	conn.closeWithProto = func(ctx context.Context, _ *v1.Proto) error {
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

func TestSessionSendIfCurrent(t *testing.T) {
	now := time.Now()
	binding := testBinding()
	stale := binding
	stale.BindingToken = "binding-stale"
	tests := []struct {
		name     string
		expected locate.GateBinding
		deadline time.Time
		matched  bool
	}{
		{name: "current binding", expected: binding, deadline: now.Add(time.Minute), matched: true},
		{name: "stale binding", expected: stale, deadline: now.Add(time.Minute)},
		{name: "expired binding", expected: binding, deadline: now.Add(-time.Millisecond)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			conn := newTestConnection(binding.ConnID)
			sess := &session{conn: conn, binding: binding, leaseDeadline: test.deadline}
			message := &v1.Proto{Op: v1.OpPush}

			matched, err := sess.sendIfCurrent(test.expected, message)

			require.NoError(t, err)
			require.Equal(t, test.matched, matched)
			if test.matched {
				require.Equal(t, message, <-conn.pushes)
				return
			}
			select {
			case <-conn.pushes:
				t.Fatal("stale push entered the connection queue")
			default:
			}
		})
	}
}

func TestSessionDetachForKick(t *testing.T) {
	now := time.Now()
	binding := testBinding()
	stale := binding
	stale.BindingToken = "binding-stale"
	tests := []struct {
		name     string
		expected locate.GateBinding
		deadline time.Time
		detached bool
	}{
		{name: "current binding", expected: binding, deadline: now.Add(time.Minute), detached: true},
		{name: "stale binding", expected: stale, deadline: now.Add(time.Minute)},
		{name: "expired binding", expected: binding, deadline: now.Add(-time.Millisecond)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sess := &session{
				conn:          newTestConnection(binding.ConnID),
				binding:       binding,
				leaseDeadline: test.deadline,
			}

			require.Equal(t, test.detached, sess.detachForKick(test.expected))
			if test.detached {
				require.Empty(t, sess.binding)
				require.True(t, sess.leaseDeadline.IsZero())
				return
			}
			require.Equal(t, binding, sess.binding)
			require.Equal(t, test.deadline, sess.leaseDeadline)
		})
	}
}

func captureGatewayLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &output
}

func newAuthenticatedServer(t *testing.T) (*Server, locate.Locator, *testConnection, locate.GateBinding) {
	t.Helper()
	store := testLocator(t)
	gateway := newTestServer(t, Locator(store), LeaseTTL(time.Minute))
	initTestGateway(t, gateway)
	conn := newTestConnection("conn-a")
	require.NoError(t, gateway.Open(context.Background(), conn))
	_, err := gateway.Handle(context.Background(), conn, authMessage(t))
	require.NoError(t, err)
	lease, err := store.LocateGate(context.Background(), "game", "player-a")
	require.NoError(t, err)
	return gateway, store, conn, lease.Binding
}
