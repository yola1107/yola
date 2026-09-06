package gateway

import (
	"context"
	"net"
	"testing"
	"time"

	"yola/api/cluster/v1"

	kgrpc "github.com/go-kratos/kratos/v3/transport/grpc"
	"github.com/stretchr/testify/require"
)

func TestBackendsSelectExactNodeByRegistryID(t *testing.T) {
	nodeA := startNamedBackendNode(t, "node-a")
	deadlines := make(chan bool, 1)
	nodeB := startBackendNode(t, &namedBackendNode{name: "node-b", deadlines: deadlines})
	discovery := newBackendTestDiscovery(
		serviceInstance("game", "node-a", nodeA),
		serviceInstance("game", "node-b", nodeB),
	)
	services := newBackends(discovery, nil, time.Second)
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
	server := kgrpc.NewServer(kgrpc.Listener(lis))
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
	services := newBackends(discovery, nil, time.Second)
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
	services := newBackends(discovery, nil, time.Second)
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
	services := newBackends(newBackendTestDiscovery(nodeA, nodeB), nil, time.Second)
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

func newBackendRequest() *v1.ForwardRequest {
	return &v1.ForwardRequest{Route: &v1.GateRoute{
		ServiceName: "game", Uid: "player-a", BindingToken: "binding-a",
		GateId: "gate-a", GateEndpoint: "grpc://127.0.0.1:9100", ConnId: "conn-a",
	}}
}
