package gateway

import (
	"context"
	"testing"
	"time"

	protocolv1 "yola/api/protocol/v1"
	"yola/locate"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
)

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
	require.NoError(t, store.BindNode(context.Background(), binding.ServiceName, binding.UID, "node-a"))

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
		identity:   identity{id: binding.GateID, endpoint: binding.GateEndpoint},
		locator:    testLocator(t),
		sessions:   &sessionRegistry{byConnID: map[string]*session{binding.ConnID: activeSession(conn, binding)}},
		rpcTimeout: time.Second,
	}
	authCtx, cancel := context.WithCancel(context.Background())
	cancel()
	gateway.kickPrevious(authCtx, binding)

	require.NoError(t, receiveWithin(t, ctxErr))
	require.True(t, isClosed(conn.closed)())
}
