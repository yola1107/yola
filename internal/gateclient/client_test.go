package gateclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"yola/api/cluster/v1"
	"yola/locate"

	"github.com/go-kratos/kratos/v3/transport/grpc"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

type gatewayStub struct {
	v1.UnimplementedGatewayServer
	pushes        chan *v1.PushRequest
	kicks         chan *v1.KickRequest
	pushDeadlines chan bool
}

func (s *gatewayStub) Push(ctx context.Context, in *v1.PushRequest) (*emptypb.Empty, error) {
	if s.pushDeadlines != nil {
		_, hasDeadline := ctx.Deadline()
		s.pushDeadlines <- hasDeadline
	}
	s.pushes <- in
	return &emptypb.Empty{}, nil
}

func (s *gatewayStub) Kick(_ context.Context, in *v1.KickRequest) (*emptypb.Empty, error) {
	s.kicks <- in
	return &emptypb.Empty{}, nil
}

func TestPushAndKickUseCurrentRoute(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := grpc.NewServer(grpc.Listener(lis), grpc.Timeout(0))
	stub := &gatewayStub{
		pushes:        make(chan *v1.PushRequest, 1),
		kicks:         make(chan *v1.KickRequest, 1),
		pushDeadlines: make(chan bool, 1),
	}
	v1.RegisterGatewayServer(server, stub)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Start(ctx) }()

	client := New(nil)
	binding := testBinding("grpc://" + lis.Addr().String())
	msg := wrapperspb.String("push")
	body, err := proto.Marshal(msg)
	require.NoError(t, err)
	require.NoError(t, client.Push(context.Background(), binding, 2, msg))
	push := <-stub.pushes
	require.Equal(t, body, push.Body)
	require.Equal(t, binding.BindingToken, push.Route.BindingToken)
	require.False(t, <-stub.pushDeadlines)

	require.NoError(t, client.Kick(context.Background(), binding, 7))
	kick := <-stub.kicks
	require.Equal(t, int32(7), kick.Code)
	require.Equal(t, binding.ConnID, kick.Route.ConnId)

	require.NoError(t, client.Close())
	cancel()
	require.NoError(t, server.Stop(context.Background()))
	require.NoError(t, <-done)
}

func TestPushUsesVerifiedTLS(t *testing.T) {
	certificateSource := httptest.NewTLSServer(http.NotFoundHandler())
	serverTLS := certificateSource.TLS.Clone()
	roots := x509.NewCertPool()
	roots.AddCert(certificateSource.Certificate())
	certificateSource.Close()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := grpc.NewServer(grpc.Listener(lis), grpc.TLSConfig(serverTLS))
	stub := &gatewayStub{pushes: make(chan *v1.PushRequest, 1)}
	v1.RegisterGatewayServer(server, stub)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		require.NoError(t, server.Stop(context.Background()))
		require.NoError(t, <-done)
	})

	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
	client := New(tlsConfig)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	tlsConfig.ServerName = "invalid-after-client-creation"
	require.NoError(t, client.Push(
		context.Background(),
		testBinding("grpcs://"+lis.Addr().String()),
		2,
		wrapperspb.String("push"),
	))
	require.NotNil(t, <-stub.pushes)
}

func TestClientReusesConcurrentConnection(t *testing.T) {
	client := New(nil)
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	const callers = 32
	rpcs := make([]*rpc, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rpcs[i], errs[i] = client.acquire(context.Background(), "grpc://127.0.0.1:1")
		}()
	}
	wg.Wait()
	for i := range callers {
		require.NoError(t, errs[i])
		require.Same(t, rpcs[0], rpcs[i])
		client.release(rpcs[i])
	}
	require.Len(t, client.byHost, 1)
}

func TestClientEvictsConnectionAfterFailedCall(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	endpoint := "grpc://" + lis.Addr().String()
	require.NoError(t, lis.Close())

	client := New(nil)
	client.idle = 10 * time.Millisecond
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.Error(t, client.Push(ctx, testBinding(endpoint), 2, wrapperspb.String("push")))
	require.Eventually(t, func() bool {
		client.mu.Lock()
		defer client.mu.Unlock()
		return len(client.byHost) == 0
	}, time.Second, time.Millisecond)
}

func TestClientEvictsConnectionCreatedAfterOnlyWaiterCanceled(t *testing.T) {
	client := New(nil)
	client.idle = 100 * time.Millisecond
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	const host = "127.0.0.1:1"

	started := make(chan struct{})
	release := make(chan struct{})
	releaseConnect := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseConnect)
	connect := client.dials.DoChan(host, func() (any, error) {
		close(started)
		<-release
		return nil, client.connect(host)
	})
	receiveGateClientValue(t, started)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.acquire(ctx, "grpc://"+host)
	require.ErrorIs(t, err, context.Canceled)

	releaseConnect()
	require.NoError(t, receiveGateClientValue(t, connect).Err)
	require.Eventually(t, func() bool {
		client.mu.Lock()
		defer client.mu.Unlock()
		return client.byHost[host] == nil
	}, time.Second, time.Millisecond)
}

func TestClientRejectsInvalidEndpoint(t *testing.T) {
	client := New(nil)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	require.ErrorIs(t, client.Push(context.Background(), locate.GateBinding{}, 1, wrapperspb.String("x")), ErrInvalidEndpoint)
}

func TestClientCloseRejectsNewWork(t *testing.T) {
	client := New(nil)
	require.NoError(t, client.Close())

	err := client.Push(context.Background(), testBinding("grpc://127.0.0.1:1"), 1, wrapperspb.String("x"))
	require.ErrorIs(t, err, ErrUnavailable)
}

func TestClientCancellationFencesCachedConnection(t *testing.T) {
	client := New(nil)
	client.byHost["127.0.0.1:1"] = &rpc{host: "127.0.0.1:1"}
	client.cancel()

	entry, err := client.cached("127.0.0.1:1")

	require.Nil(t, entry)
	require.ErrorIs(t, err, ErrUnavailable)
	delete(client.byHost, "127.0.0.1:1")
	require.NoError(t, client.Close())
}

func TestClientRejectsEndpointSecurityMismatchBeforeDial(t *testing.T) {
	tests := []struct {
		name      string
		endpoint  string
		tlsConfig *tls.Config
	}{
		{name: "secure endpoint without TLS", endpoint: "grpcs://127.0.0.1:1"},
		{
			name: "insecure endpoint with TLS", endpoint: "grpc://127.0.0.1:1",
			tlsConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := New(test.tlsConfig)
			t.Cleanup(func() { require.NoError(t, client.Close()) })

			_, err := client.acquire(context.Background(), test.endpoint)

			require.ErrorIs(t, err, ErrTransportSecurity)
			require.Empty(t, client.byHost)
		})
	}
}

func testBinding(endpoint string) locate.GateBinding {
	return locate.GateBinding{
		ServiceName: "game", UID: "player-a", BindingToken: "binding-a",
		GateID: "gate-a", GateEndpoint: endpoint, ConnID: "conn-a",
	}
}

func receiveGateClientValue[T any](t *testing.T, values <-chan T) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for gateclient test value")
		var zero T
		return zero
	}
}
