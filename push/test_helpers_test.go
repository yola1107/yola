package push_test

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	clusterv1 "yola/api/cluster/v1"
	"yola/locate"
	"yola/push"

	kgrpc "github.com/go-kratos/kratos/v3/transport/grpc"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

type lookupFunc func(context.Context, string, string) (locate.GateLease, error)

func (f lookupFunc) LocateGate(ctx context.Context, serviceName, uid string) (locate.GateLease, error) {
	return f(ctx, serviceName, uid)
}

type gatewayAdapter struct {
	clusterv1.UnimplementedGatewayServer
	pushes    chan *clusterv1.PushRequest
	pushError error
	canceled  chan error
}

func (g *gatewayAdapter) Push(ctx context.Context, request *clusterv1.PushRequest) (*emptypb.Empty, error) {
	g.pushes <- proto.CloneOf(request)
	if g.canceled != nil {
		<-ctx.Done()
		g.canceled <- ctx.Err()
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	return &emptypb.Empty{}, g.pushError
}

func newTestSender(t *testing.T, locator push.GateLocator, opts ...push.Option) *push.Sender {
	t.Helper()
	sender, err := push.NewSender(locator, opts...)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sender.Close()) })
	return sender
}

func startGateway(t *testing.T, gateway clusterv1.GatewayServer, opts ...kgrpc.ServerOption) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	ready := &readyListener{Listener: listener, accepting: make(chan struct{})}
	opts = append([]kgrpc.ServerOption{kgrpc.Listener(ready), kgrpc.Timeout(0)}, opts...)
	server := kgrpc.NewServer(opts...)
	clusterv1.RegisterGatewayServer(server, gateway)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	t.Cleanup(func() {
		cancel()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
		defer stopCancel()
		if err := server.Stop(stopCtx); err != nil {
			t.Error(err)
		}
		require.NoError(t, receive(t, done))
	})
	go func() { done <- server.Start(ctx) }()
	receive(t, ready.accepting)
	return "grpc://" + listener.Addr().String()
}

type readyListener struct {
	net.Listener
	accepting chan struct{}
	once      sync.Once
}

func (l *readyListener) Accept() (net.Conn, error) {
	l.once.Do(func() { close(l.accepting) })
	return l.Listener.Accept()
}

func testBinding(serviceName, uid, endpoint, connID, token string) locate.GateBinding {
	return locate.GateBinding{
		ServiceName:  serviceName,
		UID:          uid,
		GateID:       "gate-a",
		GateEndpoint: endpoint,
		ConnID:       connID,
		BindingToken: token,
	}
}

func receive[T any](t *testing.T, values <-chan T) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for test value")
		var zero T
		return zero
	}
}
