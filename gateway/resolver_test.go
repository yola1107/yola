package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/go-kratos/kratos/v3/registry"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/resolver"
	"google.golang.org/grpc/status"
)

func TestBackendsSelectorClearsEmptySnapshotAndRecovers(t *testing.T) {
	endpoint := startTestNode(t)
	node := serviceInstance("game", "node-a", endpoint)
	discovery := newBackendTestDiscovery(node)
	services := newBackends(discovery, nil, time.Second)
	t.Cleanup(services.close)
	client, err := backendClient(context.Background(), services, "game")
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return forwardBackend(client) == nil
	}, 3*time.Second, 10*time.Millisecond)

	discovery.publish("game")
	require.Eventually(t, func() bool {
		return status.Code(forwardBackend(client)) == codes.Unavailable
	}, 3*time.Second, 10*time.Millisecond)

	discovery.publish("game", node)
	require.Eventually(t, func() bool {
		return forwardBackend(client) == nil
	}, 3*time.Second, 10*time.Millisecond)
}

func TestBackendsRetryWatchCreation(t *testing.T) {
	endpoint := startTestNode(t)
	node := serviceInstance("game", "node-a", endpoint)
	discovery := newBackendTestDiscovery(node)
	discovery.failNextWatches(1)
	services := newBackends(discovery, nil, time.Second)
	t.Cleanup(services.close)
	client, err := backendClient(context.Background(), services, "game")
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return forwardBackend(client) == nil
	}, 4*time.Second, 10*time.Millisecond)
	require.GreaterOrEqual(t, discovery.watchCount(), 2)
}

func TestBackendResolverReportsWatchCreationCancellation(t *testing.T) {
	client := &backendResolverClient{}
	resolver := &backendResolver{
		serviceName: "game",
		discovery:   watchErrorDiscovery{err: context.Canceled},
		client:      client,
		ctx:         context.Background(),
	}

	resolver.watch()

	require.Len(t, client.errs, 1)
	require.ErrorIs(t, client.errs[0], context.Canceled)
}

func TestBackendsCloseCancelsWatchCreation(t *testing.T) {
	discovery := &blockingWatchDiscovery{
		started: make(chan struct{}),
		stopped: make(chan struct{}),
	}
	services := newBackends(discovery, nil, time.Second)
	_, err := backendClient(context.Background(), services, "game")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		select {
		case <-discovery.started:
			return true
		default:
			return false
		}
	}, time.Second, time.Millisecond)

	services.close()
	services.close()
	require.Eventually(t, func() bool {
		select {
		case <-discovery.stopped:
			return true
		default:
			return false
		}
	}, time.Second, time.Millisecond)
}

func TestBackendResolverFiltersNodes(t *testing.T) {
	nodeA := serviceInstance("game", "node-a", "grpc://127.0.0.1:9001")
	nodeB := serviceInstance("game", "node-b", "grpc://127.0.0.1:9002")
	wrongService := serviceInstance("lobby", "node-c", "grpc://127.0.0.1:9003")
	client := &backendResolverClient{}
	state := &backendState{}
	r := &backendResolver{serviceName: "game", client: client, state: state}

	r.update([]*registry.ServiceInstance{nil, nodeB, wrongService, nodeA})
	require.Len(t, client.states, 1)
	require.Equal(t, "127.0.0.1:9002", client.states[0].Addresses[0].Addr)
	require.Equal(t, "127.0.0.1:9001", client.states[0].Addresses[1].Addr)
	require.Same(t, nodeB, client.states[0].Addresses[0].Attributes.Value(rawServiceInstanceKey))
	require.Same(t, nodeA, client.states[0].Addresses[1].Attributes.Value(rawServiceInstanceKey))
	require.Contains(t, state.load().hostByNodeID, "node-a")
	require.Contains(t, state.load().hostByNodeID, "node-b")

	r.update(nil)
	require.Len(t, client.states, 2)
	require.Empty(t, client.states[1].Addresses)
	require.False(t, state.load().available)

	secure := serviceInstance("game", "node-d", "grpcs://127.0.0.1:9005")
	secureClient := &backendResolverClient{}
	secureState := &backendState{}
	secureResolver := &backendResolver{
		serviceName: "game", client: secureClient, secure: true, state: secureState,
	}
	secureResolver.update([]*registry.ServiceInstance{secure})
	require.Len(t, secureClient.states, 1)
	require.Len(t, secureClient.states[0].Addresses, 1)
	require.Equal(t, "127.0.0.1:9005", secureClient.states[0].Addresses[0].Addr)
	require.True(t, secureState.load().sticky)
}

func TestBackendResolverNodeSetKeepsExistingAddressStable(t *testing.T) {
	nodeA := serviceInstance("game", "node-a", "grpc://127.0.0.1:9001")
	nodeB := serviceInstance("game", "node-b", "grpc://127.0.0.1:9002")
	client := &backendResolverClient{}
	state := &backendState{}
	r := &backendResolver{
		serviceName: "game",
		client:      client,
		state:       state,
	}

	r.update([]*registry.ServiceInstance{nodeA})
	addressA := client.states[0].Addresses[0]
	r.update([]*registry.ServiceInstance{nodeA, nodeB})

	require.True(t, addressA.Equal(client.states[1].Addresses[0]))
	require.Contains(t, state.load().hostByNodeID, "node-b")
}

func TestBackendResolverRejectsStickyModeChangesAndRecoversOriginalMode(t *testing.T) {
	for _, test := range []struct {
		name          string
		initialSticky bool
	}{
		{name: "sticky to stateless", initialSticky: true},
		{name: "stateless to sticky"},
	} {
		t.Run(test.name, func(t *testing.T) {
			initial := serviceInstance("game", "node-a", "grpc://127.0.0.1:9001")
			changed := serviceInstance("game", "node-a", "grpc://127.0.0.1:9001")
			if test.initialSticky {
				changed.Metadata = nil
			} else {
				initial.Metadata = nil
			}
			client := &backendResolverClient{}
			state := &backendState{}
			resolver := &backendResolver{serviceName: "game", client: client, state: state}

			resolver.update([]*registry.ServiceInstance{initial})
			require.True(t, state.load().available)
			require.Equal(t, test.initialSticky, state.load().sticky)

			resolver.update(nil)
			require.Len(t, client.states, 2)
			require.Empty(t, client.states[1].Addresses)
			require.False(t, state.load().available)
			require.Equal(t, test.initialSticky, state.load().sticky)

			resolver.update([]*registry.ServiceInstance{changed})
			require.Len(t, client.states, 3)
			require.Empty(t, client.states[2].Addresses)
			require.Len(t, client.errs, 1)
			require.ErrorContains(t, client.errs[0], "changed sticky mode")
			require.ErrorContains(t, client.errs[0], "restart Gateway")
			require.False(t, state.load().available)
			require.Equal(t, test.initialSticky, state.load().sticky)

			resolver.update([]*registry.ServiceInstance{initial})
			require.Len(t, client.states, 4)
			require.Len(t, client.states[3].Addresses, 1)
			require.True(t, state.load().available)
			require.Equal(t, test.initialSticky, state.load().sticky)
		})
	}
}

func TestBackendResolverReportsEndpointSecurityMismatch(t *testing.T) {
	tests := []struct {
		name     string
		secure   bool
		endpoint string
		want     string
	}{
		{name: "secure endpoint without TLS", endpoint: "grpcs://127.0.0.1:9001", want: "grpc://"},
		{name: "insecure endpoint with TLS", secure: true, endpoint: "grpc://127.0.0.1:9001", want: "grpcs://"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &backendResolverClient{}
			state := &backendState{}
			resolver := &backendResolver{
				serviceName: "game", client: client, secure: test.secure, state: state,
			}

			resolver.update([]*registry.ServiceInstance{
				serviceInstance("game", "node-a", test.endpoint),
			})

			require.Len(t, client.states, 1)
			require.Empty(t, client.states[0].Addresses)
			require.Len(t, client.errs, 1)
			require.ErrorContains(t, client.errs[0], test.want)
			require.False(t, state.load().available)
		})
	}
}

func TestBackendResolverRejectsInconsistentServiceSnapshots(t *testing.T) {
	tests := []struct {
		name      string
		instances func() []*registry.ServiceInstance
		want      string
	}{
		{
			name: "conflicting sticky metadata",
			instances: func() []*registry.ServiceInstance {
				sticky := serviceInstance("game", "node-a", "grpc://127.0.0.1:9001")
				stateless := serviceInstance("game", "node-b", "grpc://127.0.0.1:9002")
				stateless.Metadata = nil
				return []*registry.ServiceInstance{sticky, stateless}
			},
			want: "conflicting sticky metadata",
		},
		{
			name: "sticky node without ID",
			instances: func() []*registry.ServiceInstance {
				return []*registry.ServiceInstance{serviceInstance("game", "", "grpc://127.0.0.1:9001")}
			},
			want: "empty NodeID",
		},
		{
			name: "same ID with different endpoints",
			instances: func() []*registry.ServiceInstance {
				return []*registry.ServiceInstance{
					serviceInstance("game", "node-a", "grpc://127.0.0.1:9001"),
					serviceInstance("game", "node-a", "grpc://127.0.0.1:9002"),
				}
			},
			want: "duplicate NodeID",
		},
		{
			name: "same endpoint with different IDs",
			instances: func() []*registry.ServiceInstance {
				return []*registry.ServiceInstance{
					serviceInstance("game", "node-a", "grpc://127.0.0.1:9001"),
					serviceInstance("game", "node-b", "grpc://127.0.0.1:9001"),
				}
			},
			want: "multiple NodeIDs",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &backendResolverClient{}
			state := &backendState{}
			resolver := &backendResolver{serviceName: "game", client: client, state: state}
			resolver.update([]*registry.ServiceInstance{
				serviceInstance("game", "node-valid", "grpc://127.0.0.1:9000"),
			})

			resolver.update(test.instances())

			require.Len(t, client.states, 2)
			require.Empty(t, client.states[1].Addresses)
			require.Len(t, client.errs, 1)
			require.ErrorContains(t, client.errs[0], test.want)
			require.False(t, state.load().available)
		})
	}
}

type blockingWatchDiscovery struct {
	started chan struct{}
	stopped chan struct{}
}

type watchErrorDiscovery struct {
	err error
}

type backendResolverClient struct {
	resolver.ClientConn
	states []resolver.State
	errs   []error
}

func (c *backendResolverClient) UpdateState(state resolver.State) error {
	c.states = append(c.states, state)
	return nil
}

func (c *backendResolverClient) ReportError(err error) {
	c.errs = append(c.errs, err)
}

func (*blockingWatchDiscovery) GetService(context.Context, string) ([]*registry.ServiceInstance, error) {
	return []*registry.ServiceInstance{
		serviceInstance("game", "node-a", "grpc://127.0.0.1:9001"),
	}, nil
}

func (d watchErrorDiscovery) GetService(context.Context, string) ([]*registry.ServiceInstance, error) {
	return nil, d.err
}

func (d watchErrorDiscovery) Watch(context.Context, string) (registry.Watcher, error) {
	return nil, d.err
}

func (d *blockingWatchDiscovery) Watch(ctx context.Context, _ string) (registry.Watcher, error) {
	close(d.started)
	<-ctx.Done()
	close(d.stopped)
	return nil, ctx.Err()
}
