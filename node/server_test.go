package node

import (
	"context"
	"net"
	"testing"
	"time"

	"yola/instance"

	"github.com/go-kratos/kratos/v3"
	"github.com/stretchr/testify/require"
	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

func TestMetadataFollowsNodeLocator(t *testing.T) {
	require.Nil(t, newTestServer(t).Metadata())
	server := newTestServer(t, Locator(newMemoryLocator()))
	require.Equal(t, instance.StickyMetadata(), server.Metadata())
}

func TestServerAcceptsTLSConnection(t *testing.T) {
	serverTLS, clientTLS := testTLSConfigs(t)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := newTestServer(t, Listener(lis), ServerTLS(serverTLS))
	ctx := kratos.NewContext(context.Background(), nodeTestAppInfo{})
	require.NoError(t, server.BeforeStart(ctx))
	done := make(chan error, 1)
	go func() { done <- server.Start(ctx) }()
	t.Cleanup(func() {
		require.NoError(t, server.Stop(context.Background()))
		require.NoError(t, <-done)
	})

	conn, err := grpcgo.NewClient(lis.Addr().String(), grpcgo.WithTransportCredentials(credentials.NewTLS(clientTLS)))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	requestCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	reply, err := healthpb.NewHealthClient(conn).Check(
		requestCtx, &healthpb.HealthCheckRequest{}, grpcgo.WaitForReady(true),
	)
	require.NoError(t, err)
	require.Equal(t, healthpb.HealthCheckResponse_SERVING, reply.Status)
}
