package gateway

import (
	"context"
	"errors"
	"net"
	"net/url"
	"testing"
	"time"

	protocolv1 "yola/api/protocol/v1"
	"yola/network/tcp"
	"yola/network/websocket"

	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

type gatewayRequestClient interface {
	Request(context.Context, int32, proto.Message) ([]byte, int32, error)
}

func assertGatewayRequests(ctx context.Context, t *testing.T, client gatewayRequestClient, pushes <-chan []byte, commands ...int32) {
	t.Helper()
	request := wrapperspb.String("request")
	body, err := proto.Marshal(request)
	require.NoError(t, err)
	for _, command := range commands {
		reply, code, err := client.Request(ctx, command, request)
		require.NoError(t, err)
		require.Zero(t, code)
		require.Equal(t, append([]byte("node:"), body...), reply)
	}
	var push protocolv1.Proto
	select {
	case pushBody := <-pushes:
		require.NoError(t, proto.Unmarshal(pushBody, &push))
	case <-ctx.Done():
		t.Fatal("gateway push timed out")
	}
	require.Equal(t, body, push.Body)
}

func TestNewServerWiresTCPTransport(t *testing.T) {
	store := testLocator(t)
	grpcEndpoint := startTestNode(t)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	grpcLis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	gate := newTestGateway(
		t, store, grpcEndpoint,
		Listener(grpcLis), Endpoint(&url.URL{Scheme: "grpc", Host: grpcLis.Addr().String()}),
		Transport(tcp.NewServer(tcp.Listener(lis))),
	)
	startTestApp(t, gate)

	connected := make(chan struct{})
	pushes := make(chan []byte, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	client, err := tcp.NewClient(ctx,
		tcp.WithAddress(lis.Addr().String()),
		tcp.WithServiceName("game"),
		tcp.WithToken("synthetic-token"),
		tcp.WithConnectFunc(func() { close(connected) }),
		tcp.WithPushHandler(map[int32]tcp.PushHandler{3: func(body []byte) { pushes <- body }}),
	)
	require.NoError(t, err)
	t.Cleanup(client.Close)
	select {
	case <-connected:
	case <-ctx.Done():
		t.Fatal("TCP authentication timed out")
	}

	assertGatewayRequests(ctx, t, client, pushes, 1, 2)
}

func TestNewServerWiresWebSocketTransport(t *testing.T) {
	store := testLocator(t)
	grpcEndpoint := startTestNode(t)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	grpcLis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	channelConfig := &websocket.ChannelConfig{
		WriteTimeout:  time.Second,
		ReadDeadline:  time.Second,
		SendQueueSize: 32,
	}
	heartbeats := make(chan struct{}, 2)
	wsServer := websocket.NewServer(
		websocket.Listener(lis), websocket.ServerChannelConfig(channelConfig), websocket.HandshakeTimeout(time.Second),
		websocket.Middleware(func(next middleware.Handler) middleware.Handler {
			return func(ctx context.Context, req any) (any, error) {
				reply, handleErr := next(ctx, req)
				if message, ok := reply.(*protocolv1.Proto); handleErr == nil && ok && message.Op == protocolv1.OpHeartbeatReply {
					select {
					case heartbeats <- struct{}{}:
					default:
					}
				}
				return reply, handleErr
			}
		}),
	)
	gate := newTestGateway(
		t, store, grpcEndpoint,
		Listener(grpcLis), Endpoint(&url.URL{Scheme: "grpc", Host: grpcLis.Addr().String()}),
		Transport(wsServer),
	)
	startTestApp(t, gate)

	connected := make(chan struct{})
	pushes := make(chan []byte, 1)
	client, err := websocket.NewClient(context.Background(),
		websocket.WithEndpoint("ws://"+lis.Addr().String()),
		websocket.WithServiceName("game"),
		websocket.WithToken("synthetic-token"),
		websocket.WithChannelConfig(channelConfig),
		websocket.WithPingInterval(100*time.Millisecond),
		websocket.WithConnectFunc(func(*websocket.Channel) { close(connected) }),
		websocket.WithPushHandler(map[int32]websocket.PushHandler{3: func(body []byte) { pushes <- body }}),
	)
	require.NoError(t, err)
	defer client.Close()
	select {
	case <-connected:
	case <-time.After(time.Second):
		t.Fatal("websocket authentication timed out")
	}
	requestCtx, cancelRequest := context.WithTimeout(context.Background(), time.Second)
	defer cancelRequest()
	assertGatewayRequests(requestCtx, t, client, pushes, 2)
	// 第二轮心跳证明客户端已确认第一轮回复，不依赖固定 sleep 猜测处理进度。
	for range 2 {
		select {
		case <-heartbeats:
		case <-requestCtx.Done():
			t.Fatal("websocket heartbeat timed out")
		}
	}
	require.True(t, client.IsAlive(), "websocket heartbeat closed an authenticated connection")
}

func TestServerAcceptsTLSConnection(t *testing.T) {
	serverTLS, clientTLS := testTLSConfigs(t)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	endpoint := &url.URL{Scheme: "grpcs", Host: lis.Addr().String()}
	server := newTestServer(t, Listener(lis), Endpoint(endpoint), ServerTLS(serverTLS))
	startTestApp(t, server)

	conn, err := grpc.NewClient(endpoint.Host, grpc.WithTransportCredentials(credentials.NewTLS(clientTLS)))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	reply, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{}, grpc.WaitForReady(true))
	require.NoError(t, err)
	require.Equal(t, healthpb.HealthCheckResponse_SERVING, reply.Status)
}

func TestGatewayConstructionReportsSetHandlerFailure(t *testing.T) {
	handlerErr := errors.New("handler rejected")
	grpcLis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, grpcLis.Close()) })
	_, err = NewServer(
		Auth(testAuthenticator{}),
		Locator(testLocator(t)),
		Discovery(staticDiscovery{}),
		Listener(grpcLis),
		Transport(&stubTransport{handlerErr: handlerErr}),
	)
	require.ErrorIs(t, err, handlerErr)
}
