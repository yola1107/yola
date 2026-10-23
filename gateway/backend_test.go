package gateway

import (
	"context"
	"crypto/tls"
	"net"
	"sync"
	"testing"
	"time"

	v1 "yola/api/cluster/v1"
	"yola/instance"
	"yola/internal/clusterroute"

	"github.com/go-kratos/kratos/v3/registry"
	"github.com/go-kratos/kratos/v3/transport/grpc"
	"github.com/stretchr/testify/require"
)

func TestBackendsCacheClientByService(t *testing.T) {
	discovery := newBackendTestDiscovery(
		serviceInstance("game", "node-a", "grpc://127.0.0.1:9001"),
		serviceInstance("lobby", "node-a", "grpc://127.0.0.1:9002"),
	)
	services := newBackends(discovery, nil, time.Second, nil)
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
	services := newBackends(discovery, nil, time.Second, nil)
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

func TestBackendsCloseCancelsSharedConnection(t *testing.T) {
	discovery := &blockingBackendDiscovery{
		backendTestDiscovery: newBackendTestDiscovery(
			serviceInstance("game", "node-a", "grpc://127.0.0.1:9001"),
		),
		started: make(chan context.Context, 1),
		release: make(chan struct{}),
	}
	services := newBackends(discovery, nil, time.Second, nil)
	result := make(chan error, 1)
	go func() {
		_, err := services.get(context.Background(), "game")
		result <- err
	}()
	receiveWithin(t, discovery.started)
	services.close()
	require.ErrorIs(t, receiveWithin(t, result), errBackendsClosed)
}

func TestBackendsRejectStickyModeChange(t *testing.T) {
	registered := serviceInstance("game", "node-a", "grpc://127.0.0.1:9001")
	registered.Metadata = nil
	discovery := newBackendTestDiscovery(registered)
	services := newBackends(discovery, nil, time.Second, nil)
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
	endpoint := startBackendNodeWithOptions(t, "grpcs", &namedBackendNode{name: "node-a"}, grpc.TLSConfig(serverTLS))
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
			}, time.Second, nil)
			t.Cleanup(services.close)
			client, err := backendClient(context.Background(), services, test.service)
			require.NoError(t, err)

			require.Eventually(t, func() bool {
				return forwardBackend(client) == nil
			}, 3*time.Second, 10*time.Millisecond)
		})
	}
}

func TestBackendsSelectExactNodeByRegistryID(t *testing.T) {
	nodeA := startNamedBackendNode(t, "node-a")
	deadlines := make(chan bool, 1)
	nodeB := startBackendNode(t, &namedBackendNode{name: "node-b", deadlines: deadlines})
	discovery := newBackendTestDiscovery(
		serviceInstance("game", "node-a", nodeA),
		serviceInstance("game", "node-b", nodeB),
	)
	services := newBackends(discovery, nil, time.Second, nil)
	t.Cleanup(services.close)

	client, err := backendClient(context.Background(), services, "game")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		reply, callErr := client.Forward(withNodeID(context.Background(), "node-b"), newBackendRequest())
		return callErr == nil && string(reply.Body) == "node-b:"
	}, 3*time.Second, 10*time.Millisecond)
	require.False(t, <-deadlines)
	cached, err := backendClient(context.Background(), services, "game")
	require.NoError(t, err)
	require.Same(t, client, cached)
	require.Len(t, services.byService, 1)
}

func TestBackendsExactNodeWaitsUntilRegisteredNodeIsReady(t *testing.T) {
	nodeA := startNamedBackendNode(t, "node-a")
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := grpc.NewServer(grpc.Listener(lis))
	v1.RegisterNodeServer(server, &namedBackendNode{name: "node-b"})
	done := make(chan error, 1)
	started := false
	t.Cleanup(func() {
		if !started {
			require.NoError(t, lis.Close())
			return
		}
		require.NoError(t, server.Stop(context.Background()))
		requireBackendServerExit(t, <-done)
	})

	discovery := newBackendTestDiscovery(
		serviceInstance("game", "node-a", nodeA),
		serviceInstance("game", "node-b", "grpc://"+lis.Addr().String()),
	)
	services := newBackends(discovery, nil, time.Second, nil)
	t.Cleanup(services.close)
	client, err := backendClient(context.Background(), services, "game")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		_, callErr := client.Forward(withNodeID(context.Background(), "node-a"), newBackendRequest())
		return callErr == nil
	}, 3*time.Second, 10*time.Millisecond)

	type result struct {
		reply *v1.ForwardReply
		err   error
	}
	results := make(chan result, 1)
	go func() {
		reply, callErr := client.Forward(withNodeID(context.Background(), "node-b"), newBackendRequest())
		results <- result{reply: reply, err: callErr}
	}()
	select {
	case early := <-results:
		require.NoError(t, early.err, "registered Node returned before becoming READY")
	case <-time.After(100 * time.Millisecond):
	}

	started = true
	go func() { done <- server.Start(context.Background()) }()
	select {
	case completed := <-results:
		require.NoError(t, completed.err)
		require.Equal(t, []byte("node-b:"), completed.reply.Body)
	case <-time.After(3 * time.Second):
		t.Fatal("exact Node request did not resume after the Node became READY")
	}
}

func TestBackendsExactNodeFollowsDiscoveryUpdate(t *testing.T) {
	oldEndpoint := startNamedBackendNode(t, "old")
	newEndpoint := startNamedBackendNode(t, "new")
	discovery := newBackendTestDiscovery(serviceInstance("game", "node-a", oldEndpoint))
	services := newBackends(discovery, nil, time.Second, nil)
	t.Cleanup(services.close)

	client, err := backendClient(context.Background(), services, "game")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		reply, callErr := client.Forward(withNodeID(context.Background(), "node-a"), newBackendRequest())
		return callErr == nil && string(reply.Body) == "old:"
	}, 3*time.Second, 10*time.Millisecond)

	discovery.publish("game", serviceInstance("game", "node-a", newEndpoint))
	require.Eventually(t, func() bool {
		reply, callErr := client.Forward(withNodeID(context.Background(), "node-a"), newBackendRequest())
		return callErr == nil && string(reply.Body) == "new:"
	}, 3*time.Second, 10*time.Millisecond)
}

func TestBackendsWeightedRoundRobin(t *testing.T) {
	nodeA := serviceInstance("game", "node-a", startNamedBackendNode(t, "node-a"))
	nodeA.Metadata["weight"] = "1"
	nodeB := serviceInstance("game", "node-b", startNamedBackendNode(t, "node-b"))
	nodeB.Metadata["weight"] = "3"
	services := newBackends(newBackendTestDiscovery(nodeA, nodeB), nil, time.Second, nil)
	t.Cleanup(services.close)
	client, err := backendClient(context.Background(), services, "game")
	require.NoError(t, err)

	seen := make(map[string]bool)
	require.Eventually(t, func() bool {
		reply, callErr := client.Forward(context.Background(), newBackendRequest())
		if callErr == nil {
			seen[string(reply.Body)] = true
		}
		return seen["node-a:"] && seen["node-b:"]
	}, 3*time.Second, 10*time.Millisecond)

	counts := make(map[string]int)
	for range 40 {
		reply, callErr := client.Forward(context.Background(), newBackendRequest())
		require.NoError(t, callErr)
		counts[string(reply.Body)]++
	}
	require.Equal(t, 10, counts["node-a:"])
	require.Equal(t, 30, counts["node-b:"])
}

func TestRPCTimeoutDoesNotCancelBackendCreation(t *testing.T) {
	discovery := &blockingBackendDiscovery{
		backendTestDiscovery: newBackendTestDiscovery(serviceInstance("game", "node-a", "grpc://127.0.0.1:9001")),
		started:              make(chan context.Context, 1),
		release:              make(chan struct{}),
	}
	gate := newTestServer(t, Discovery(discovery), RPCTimeout(time.Nanosecond))
	t.Cleanup(func() { require.NoError(t, gate.Stop(context.Background())) })
	unblock := sync.OnceFunc(func() { close(discovery.release) })
	t.Cleanup(unblock)
	created := make(chan error, 1)
	go func() {
		_, err := gate.backends.get(context.Background(), "game")
		created <- err
	}()
	connectCtx := receiveWithin(t, discovery.started)
	deadline, ok := connectCtx.Deadline()
	require.True(t, ok)
	require.Greater(t, time.Until(deadline), time.Second)
	require.NoError(t, connectCtx.Err())
	unblock()
	require.NoError(t, receiveWithin(t, created))
}

func TestConnectTimeoutBoundsSharedCreation(t *testing.T) {
	discovery := &blockingBackendDiscovery{
		backendTestDiscovery: newBackendTestDiscovery(serviceInstance("game", "node-a", "grpc://127.0.0.1:9001")),
		started:              make(chan context.Context, 1),
		release:              make(chan struct{}),
	}
	gate := newTestServer(t, Discovery(discovery), RPCTimeout(time.Minute), ConnectTimeout(20*time.Millisecond))
	t.Cleanup(func() { require.NoError(t, gate.Stop(context.Background())) })
	created := make(chan error, 1)
	go func() {
		_, err := gate.backends.get(context.Background(), "game")
		created <- err
	}()
	receiveWithin(t, discovery.started)
	require.ErrorIs(t, receiveWithin(t, created), context.DeadlineExceeded)
}

func newBackendRequest() *v1.ForwardRequest {
	return &v1.ForwardRequest{Route: clusterroute.FromBinding(testBinding())}
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
