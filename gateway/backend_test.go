package gateway

import (
	"context"
	"crypto/tls"
	"sync"
	"testing"
	"time"

	"yola/api/cluster/v1"
	"yola/instance"

	"github.com/go-kratos/kratos/v3/registry"
	kgrpc "github.com/go-kratos/kratos/v3/transport/grpc"
	"github.com/stretchr/testify/require"
)

func TestBackendsCacheClientByService(t *testing.T) {
	discovery := newBackendTestDiscovery(
		serviceInstance("game", "node-a", "grpc://127.0.0.1:9001"),
		serviceInstance("lobby", "node-a", "grpc://127.0.0.1:9002"),
	)
	services := newBackends(discovery, nil, time.Second)
	t.Cleanup(services.close)

	const callers = 16
	clients := make(chan v1.NodeClient, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			client, err := backendClient(context.Background(), services, "game")
			clients <- client
			errs <- err
		}()
	}
	wg.Wait()
	close(clients)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	var first v1.NodeClient
	for client := range clients {
		if first == nil {
			first = client
			continue
		}
		require.Same(t, first, client)
	}
	other, err := backendClient(context.Background(), services, "lobby")
	require.NoError(t, err)

	require.NotSame(t, first, other)
	require.Len(t, services.byService, 2)
}

func TestBackendsConnectionOutlivesFirstWaiter(t *testing.T) {
	discovery := &blockingBackendDiscovery{
		backendTestDiscovery: newBackendTestDiscovery(
			serviceInstance("game", "node-a", "grpc://127.0.0.1:9001"),
		),
		started: make(chan context.Context, 1),
		release: make(chan struct{}),
	}
	services := newBackends(discovery, nil, time.Second)
	t.Cleanup(services.close)

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() {
		_, err := services.get(firstCtx, "game")
		first <- err
	}()
	connectCtx := receiveWithin(t, discovery.started)
	_, hasDeadline := connectCtx.Deadline()
	require.True(t, hasDeadline)
	cancelFirst()
	require.ErrorIs(t, receiveWithin(t, first), context.Canceled)
	require.NoError(t, connectCtx.Err())

	second := make(chan error, 1)
	go func() {
		_, err := services.get(context.Background(), "game")
		second <- err
	}()
	close(discovery.release)
	require.NoError(t, receiveWithin(t, second))
	cached, err := services.cached("game")
	require.NoError(t, err)
	require.NotNil(t, cached)
}

func TestBackendsBoundConnectionAndCancelOnClose(t *testing.T) {
	for _, test := range []struct {
		name    string
		timeout time.Duration
		close   bool
		want    error
	}{
		{name: "RPC timeout", timeout: 20 * time.Millisecond, want: context.DeadlineExceeded},
		{name: "pool close", timeout: time.Second, close: true, want: errBackendsClosed},
	} {
		t.Run(test.name, func(t *testing.T) {
			discovery := &blockingBackendDiscovery{
				backendTestDiscovery: newBackendTestDiscovery(
					serviceInstance("game", "node-a", "grpc://127.0.0.1:9001"),
				),
				started: make(chan context.Context, 1),
				release: make(chan struct{}),
			}
			services := newBackends(discovery, nil, test.timeout)
			result := make(chan error, 1)
			go func() {
				_, err := services.get(context.Background(), "game")
				result <- err
			}()
			receiveWithin(t, discovery.started)
			if test.close {
				services.close()
			} else {
				t.Cleanup(services.close)
			}
			require.ErrorIs(t, receiveWithin(t, result), test.want)
		})
	}
}

func TestBackendsRejectStickyModeChange(t *testing.T) {
	registered := serviceInstance("game", "node-a", "grpc://127.0.0.1:9001")
	registered.Metadata = nil
	discovery := newBackendTestDiscovery(registered)
	services := newBackends(discovery, nil, time.Second)
	t.Cleanup(services.close)

	service, err := services.get(context.Background(), "game")
	require.NoError(t, err)
	require.False(t, service.state.load().sticky)
	discovery.publish("game", serviceInstance("game", "node-a", "grpc://127.0.0.1:9001"))
	service, err = services.get(context.Background(), "game")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		snapshot := service.state.load()
		return !snapshot.available && !snapshot.sticky
	}, time.Second, time.Millisecond)

	recovered := serviceInstance("game", "node-a", "grpc://127.0.0.1:9001")
	recovered.Metadata = nil
	discovery.publish("game", recovered)
	require.Eventually(t, func() bool {
		snapshot := service.state.load()
		return snapshot.available && !snapshot.sticky
	}, time.Second, time.Millisecond)

	invalid := serviceInstance("lobby", "node-a", "grpc://127.0.0.1:9002")
	invalid.Metadata = map[string]string{instance.StickyMetadataKey: "invalid"}
	discovery.publish("lobby", invalid)
	_, err = services.get(context.Background(), "lobby")
	require.ErrorContains(t, err, "invalid sticky metadata")

	conflict := serviceInstance("mixed", "node-a", "grpc://127.0.0.1:9003")
	stateless := serviceInstance("mixed", "node-b", "grpc://127.0.0.1:9004")
	stateless.Metadata = nil
	discovery.publish("mixed", conflict, stateless)
	_, err = services.get(context.Background(), "mixed")
	require.ErrorContains(t, err, "conflicting sticky metadata")
}

func TestBackendsVerifiedTLSAuthority(t *testing.T) {
	serverTLS, clientTLS := testTLSConfigs(t)
	endpoint := startBackendNodeWithOptions(t, "grpcs", &tlsBackendNode{}, kgrpc.TLSConfig(serverTLS))
	tests := []struct {
		name       string
		service    string
		serverName string
	}{
		{name: "explicit TLS server name", service: "game", serverName: "example.com"},
		{name: "service authority by default", service: "example.com"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			discovery := newBackendTestDiscovery(
				serviceInstance(test.service, "node-a", endpoint),
			)
			services := newBackends(discovery, &tls.Config{
				MinVersion: tls.VersionTLS12,
				RootCAs:    clientTLS.RootCAs,
				ServerName: test.serverName,
			}, time.Second)
			t.Cleanup(services.close)
			client, err := backendClient(context.Background(), services, test.service)
			require.NoError(t, err)

			require.Eventually(t, func() bool {
				return forwardBackend(client) == nil
			}, 3*time.Second, 10*time.Millisecond)
		})
	}
}

func TestGRPCTLSValidatesAndClonesConfig(t *testing.T) {
	var nilOptions options
	require.EqualError(t, ClientTLS(nil)(&nilOptions), "gateway: gRPC TLS config is required")

	config := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: "node.internal"}
	var configured options
	require.NoError(t, ClientTLS(config)(&configured))
	require.NotSame(t, config, configured.clientTLS)
	require.Equal(t, config.ServerName, configured.clientTLS.ServerName)
}

type tlsBackendNode struct {
	v1.UnimplementedNodeServer
}

type blockingBackendDiscovery struct {
	*backendTestDiscovery
	started chan context.Context
	release chan struct{}
}

func (d *blockingBackendDiscovery) GetService(ctx context.Context, service string) ([]*registry.ServiceInstance, error) {
	d.started <- ctx
	select {
	case <-d.release:
		return d.backendTestDiscovery.GetService(ctx, service)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (*tlsBackendNode) Forward(context.Context, *v1.ForwardRequest) (*v1.ForwardReply, error) {
	return &v1.ForwardReply{}, nil
}
