package gateway

import (
	"context"
	"testing"
	"time"

	protocolv1 "yola/api/protocol/v1"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
)

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
