package gateway

import (
	"context"
	"sync"
	"testing"
	"time"

	clusterv1 "yola/api/cluster/v1"
	protocolv1 "yola/api/protocol/v1"
	"yola/locate"
	"yola/network"

	kgrpc "github.com/go-kratos/kratos/v3/transport/grpc"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRPCTimeoutDoesNotCancelCleanup(t *testing.T) {
	gate := newTestServer(t, RPCTimeout(time.Nanosecond))
	t.Cleanup(func() { require.NoError(t, gate.Stop(context.Background())) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cleanupCtx, cancelCleanup := gate.cleanupContext(ctx)
	defer cancelCleanup()
	require.NoError(t, cleanupCtx.Err())
	deadline, ok := cleanupCtx.Deadline()
	require.True(t, ok)
	require.Greater(t, time.Until(deadline), time.Second)
}

func TestRPCTimeoutDoesNotCancelLeaseRenewal(t *testing.T) {
	base := testLocator(t)
	store := &contextCheckedRenewLocator{Locator: base}
	gate := newTestServer(t, Locator(store), RPCTimeout(time.Nanosecond))
	t.Cleanup(func() { require.NoError(t, gate.Stop(context.Background())) })
	lease, _, err := base.BindGate(context.Background(), testBinding(), time.Minute)
	require.NoError(t, err)
	sess := activeSession(newTestConnection("conn-a"), lease.Binding)
	deadline := time.Now().Add(gate.leaseTTL / 2)
	sess.leaseDeadline = deadline
	require.NoError(t, gate.heartbeat(context.Background(), sess))
	require.Greater(t, store.remaining, time.Second)
	require.True(t, sess.leaseDeadline.After(deadline), "request timeout must not prevent independent renewal")
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

func TestLeaseTimeoutPreservesShorterParent(t *testing.T) {
	for _, test := range []struct {
		name   string
		parent time.Duration
		lease  time.Duration
		limit  time.Duration
	}{
		{name: "parent", parent: time.Second, lease: 2 * time.Second, limit: time.Second},
		{name: "lease", parent: 2 * time.Second, lease: time.Second, limit: time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := testLocator(t)
			store := &contextCheckedRenewLocator{Locator: base}
			gate := newTestServer(t, Locator(store), RPCTimeout(time.Nanosecond), LeaseTimeout(test.lease))
			t.Cleanup(func() { require.NoError(t, gate.Stop(context.Background())) })
			lease, _, err := base.BindGate(context.Background(), testBinding(), time.Minute)
			require.NoError(t, err)
			sess := activeSession(newTestConnection("conn-a"), lease.Binding)
			sess.leaseDeadline = time.Now().Add(gate.leaseTTL / 2)
			ctx, cancel := context.WithTimeout(context.Background(), test.parent)
			defer cancel()
			require.NoError(t, gate.heartbeat(ctx, sess))
			require.Greater(t, store.remaining, test.limit/2)
			require.LessOrEqual(t, store.remaining, test.limit)
		})
	}
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

func TestTimeoutOptionsRejectNonPositive(t *testing.T) {
	for _, test := range []struct {
		name   string
		option func(time.Duration) Option
	}{
		{name: "connect", option: ConnectTimeout},
		{name: "lease", option: LeaseTimeout},
		{name: "cleanup", option: CleanupTimeout},
	} {
		for _, timeout := range []time.Duration{0, -time.Second} {
			t.Run(test.name+"/"+timeout.String(), func(t *testing.T) {
				_, err := resolveOptions(Auth(testAuthenticator{}), Locator(pingLocator{}), Discovery(staticDiscovery{}), test.option(timeout))
				require.Error(t, err)
			})
		}
	}
}

func TestForwardBudgetUsesShortestDeadlineAcrossGRPC(t *testing.T) {
	for _, test := range []struct {
		name      string
		transport time.Duration
		rpc       time.Duration
		node      time.Duration
		parent    time.Duration
		limit     time.Duration
	}{
		{name: "transport", transport: 250 * time.Millisecond, rpc: time.Second, node: time.Second, limit: 250 * time.Millisecond},
		{name: "forward", transport: time.Second, rpc: 200 * time.Millisecond, node: time.Second, limit: 200 * time.Millisecond},
		{name: "node", transport: time.Second, rpc: time.Second, node: 150 * time.Millisecond, limit: 150 * time.Millisecond},
		{name: "parent", transport: time.Second, rpc: time.Second, node: time.Second, parent: 100 * time.Millisecond, limit: 100 * time.Millisecond},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := &deadlineBackendNode{budgets: make(chan time.Duration, 1)}
			endpoint := startBackendNodeWithOptions(t, "grpc", backend, kgrpc.Timeout(test.node))
			gate := newTestGateway(t, testLocator(t), endpoint, RPCTimeout(test.rpc))
			t.Cleanup(func() { require.NoError(t, gate.Stop(context.Background())) })
			client, err := backendClient(context.Background(), gate.backends, "game")
			require.NoError(t, err)
			_, err = client.Forward(context.Background(), &clusterv1.ForwardRequest{})
			require.NoError(t, err)
			conn := newTestConnection("conn-a")
			gate.sessions.byConnID[conn.ConnID()] = activeSession(conn, testBinding())
			ctx := context.Background()
			if test.parent > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, test.parent)
				defer cancel()
			}
			invoke := network.NewInvoker(gate, conn, test.transport)
			reply, err := invoke(ctx, &protocolv1.Proto{Op: protocolv1.OpRequest, Cmd: 1})
			require.NoError(t, err)
			require.Equal(t, protocolv1.OpResponse, reply.Op)
			require.Equal(t, int32(codes.DeadlineExceeded), reply.Code)
			remaining := receiveWithin(t, backend.budgets)
			require.Greater(t, remaining, test.limit/2)
			require.LessOrEqual(t, remaining, test.limit+5*time.Millisecond)
		})
	}
}

type deadlineBackendNode struct {
	clusterv1.UnimplementedNodeServer
	budgets chan time.Duration
}

func (n *deadlineBackendNode) Forward(ctx context.Context, request *clusterv1.ForwardRequest) (*clusterv1.ForwardReply, error) {
	if request.Command == 0 {
		return &clusterv1.ForwardReply{}, nil
	}
	deadline, _ := ctx.Deadline()
	n.budgets <- time.Until(deadline)
	<-ctx.Done()
	return nil, status.FromContextError(ctx.Err()).Err()
}

type contextCheckedRenewLocator struct {
	locate.Locator
	remaining time.Duration
}

func (l *contextCheckedRenewLocator) RenewGateLease(ctx context.Context, binding locate.GateBinding, ttl time.Duration) (locate.GateLease, error) {
	if deadline, ok := ctx.Deadline(); ok {
		l.remaining = time.Until(deadline)
	}
	if err := ctx.Err(); err != nil {
		return locate.GateLease{}, err
	}
	return l.Locator.RenewGateLease(ctx, binding, ttl)
}
