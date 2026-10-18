package gateway

import (
	"context"
	"errors"
	"sync/atomic"
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

func TestStickyRoutesUseEpochOnlyForForward(t *testing.T) {
	store := &epochCountingLocator{Locator: testLocator(t)}
	disconnects := make(chan *clusterv1.DisconnectRequest, 1)
	service := &forwardServer{disconnects: disconnects}
	nodeA := startBackendNode(t, service)
	gate := newTestServer(t,
		Locator(store),
		Discovery(staticDiscovery{"game": {serviceInstance("game", "node-a", nodeA)}}),
	)
	initTestGateway(t, gate)

	binding := locate.GateBinding{
		ServiceName:  "game",
		UID:          "player-a",
		GateID:       "gate-a",
		GateEndpoint: "grpc://127.0.0.1:9100",
		ConnID:       "conn-a",
		BindingToken: "token-a",
	}
	bindTestPlayerNode(t, store.Locator, "game", "player-a", "node-a")

	ctx := context.Background()
	message := &protocolv1.Proto{Op: protocolv1.OpRequest, Cmd: 1}
	gate.forward(ctx, activeSession(newTestConnection(binding.ConnID), binding), message)
	require.Equal(t, int32(codes.OK), message.Code)
	require.Equal(t, "node-a", service.request.NodeId)
	require.NotEmpty(t, service.request.NodeEpoch)
	require.Equal(t, int32(1), store.epochLookups.Load())

	store.epochLookups.Store(0)
	gate.notifyDisconnect(ctx, binding)
	disconnect := receiveWithin(t, disconnects)
	require.Equal(t, binding.UID, disconnect.GetRoute().GetUid())
	require.Equal(t, binding.BindingToken, disconnect.GetRoute().GetBindingToken())
	require.Zero(t, store.epochLookups.Load())
}

func TestForwardStatusCode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want codes.Code
	}{
		{name: "canceled", err: context.Canceled, want: codes.Canceled},
		{name: "deadline", err: context.DeadlineExceeded, want: codes.DeadlineExceeded},
		{name: "invalid binding", err: locate.ErrInvalidNodeBinding, want: codes.Internal},
		{name: "invalid epoch", err: locate.ErrInvalidNodeEpoch, want: codes.Internal},
		{name: "epoch missing", err: locate.ErrNodeEpochNotFound, want: codes.Aborted},
		{name: "epoch conflict", err: locate.ErrNodeEpochConflict, want: codes.Aborted},
		{name: "dial failure", err: errors.New("connection refused"), want: codes.Unavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, forwardStatusCode(tc.err))
		})
	}
}

func TestForwardReturnsLocatorStatusInProtocolReply(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want codes.Code
	}{
		{name: "invalid epoch", err: locate.ErrInvalidNodeEpoch, want: codes.Internal},
		{name: "epoch missing", err: locate.ErrNodeEpochNotFound, want: codes.Aborted},
		{name: "RPC error during route lookup", err: status.Error(codes.ResourceExhausted, "request rejected"), want: codes.Unavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &failingEpochLocator{Locator: testLocator(t), err: test.err}
			gate := newTestServer(t,
				Locator(store),
				Discovery(staticDiscovery{"game": {serviceInstance("game", "node-a", "grpc://127.0.0.1:1")}}),
			)
			initTestGateway(t, gate)
			binding := testBinding()
			bindTestPlayerNode(t, store.Locator, binding.ServiceName, binding.UID, "node-a")
			message := &protocolv1.Proto{Op: protocolv1.OpRequest, Cmd: 1}

			gate.forward(context.Background(), activeSession(newTestConnection(binding.ConnID), binding), message)

			require.Equal(t, protocolv1.OpResponse, message.Op)
			require.Equal(t, int32(test.want), message.Code)
			require.Nil(t, message.Body)
		})
	}
}

func TestForwardNodeLookupHonorsRequestContext(t *testing.T) {
	for _, test := range []struct {
		name string
		ctx  func() context.Context
		code codes.Code
	}{
		{name: "timeout", ctx: context.Background, code: codes.DeadlineExceeded},
		{name: "canceled", ctx: func() context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx
		}, code: codes.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &blockingNodeLocator{Locator: testLocator(t)}
			backendPool := newBackends(staticDiscovery{"game": {
				serviceInstance("game", "node-a", "grpc://127.0.0.1:1"),
			}}, nil, 20*time.Millisecond, store)
			t.Cleanup(backendPool.close)
			gateway := &Server{
				backends:   backendPool,
				rpcTimeout: 20 * time.Millisecond,
			}
			request := &protocolv1.Proto{Op: protocolv1.OpRequest}

			started := time.Now()
			gateway.forward(test.ctx(), activeSession(newTestConnection("conn-a"), testBinding()), request)

			require.Equal(t, int32(test.code), request.Code)
			require.Less(t, time.Since(started), 200*time.Millisecond)
		})
	}
}

func TestForwardRejectsExpiredBinding(t *testing.T) {
	binding := testBinding()
	conn := newTestConnection(binding.ConnID)
	sess := activeSession(conn, binding)
	sess.leaseDeadline = time.Now().Add(-time.Millisecond)
	request := &protocolv1.Proto{Op: protocolv1.OpRequest, Body: []byte("request")}

	(&Server{}).forward(context.Background(), sess, request)

	require.Equal(t, protocolv1.OpResponse, request.Op)
	require.Equal(t, int32(codes.Unauthenticated), request.Code)
	require.Nil(t, request.Body)
	require.True(t, isClosed(conn.closed)())
}

func TestForwardCarriesRouteAndMapsNodeError(t *testing.T) {
	for _, code := range []codes.Code{codes.Aborted, codes.ResourceExhausted} {
		t.Run(code.String(), func(t *testing.T) {
			service := &forwardServer{err: status.Error(code, "request rejected")}
			gateway := newForwardTestGateway(t, service)
			conn := newTestConnection("conn-a")
			binding := testBinding()
			sess := activeSession(conn, binding)
			var request *protocolv1.Proto
			require.Eventually(t, func() bool {
				request = &protocolv1.Proto{Op: protocolv1.OpRequest, Cmd: 7, Body: []byte("request")}
				gateway.forward(context.Background(), sess, request)
				return request.Code == int32(code)
			}, time.Second, time.Millisecond)

			require.Equal(t, int32(code), request.Code)
			require.False(t, isClosed(conn.closed)())
			require.NotNil(t, service.request)
			require.Equal(t, binding.ServiceName, service.request.GetRoute().GetServiceName())
			require.Equal(t, binding.BindingToken, service.request.GetRoute().GetBindingToken())
			require.Equal(t, binding.GateEndpoint, service.request.GetRoute().GetGateEndpoint())
		})
	}
}

func TestCloseNotifiesNodeDisconnect(t *testing.T) {
	gateway, store, conn, binding, disconnects := newDisconnectTrackingServer(t)

	gateway.Close(context.Background(), conn)
	select {
	case got := <-disconnects:
		require.Equal(t, binding.UID, got.GetRoute().GetUid())
		require.Equal(t, binding.BindingToken, got.GetRoute().GetBindingToken())
	case <-time.After(2 * time.Second):
		t.Fatal("disconnect was not delivered")
	}
	_, err := store.LocateGate(context.Background(), binding.ServiceName, binding.UID)
	require.ErrorIs(t, err, locate.ErrGateNotFound)
}

func TestForwardMapsOversizedReplyToResourceExhausted(t *testing.T) {
	service := &forwardServer{body: make([]byte, protocolv1.MaxProtoSize)}
	gateway := newForwardTestGateway(t, service)
	request := &protocolv1.Proto{Op: protocolv1.OpRequest}
	sess := activeSession(newTestConnection("conn-a"), testBinding())

	require.Eventually(t, func() bool {
		gateway.forward(context.Background(), sess, request)
		return request.Code != int32(codes.Unavailable)
	}, time.Second, time.Millisecond)

	require.Equal(t, protocolv1.OpResponse, request.Op)
	require.Equal(t, int32(codes.ResourceExhausted), request.Code)
	require.Empty(t, request.Body)
}

func TestGatewayRoutesBoundPlayerToExactNode(t *testing.T) {
	store := testLocator(t)
	nodeA := startNamedBackendNode(t, "node-a")
	nodeB := startNamedBackendNode(t, "node-b")
	gate := newTestServer(t,
		Locator(store),
		Discovery(staticDiscovery{"game": {
			serviceInstance("game", "node-a", nodeA),
			serviceInstance("game", "node-b", nodeB),
		}}),
	)
	initTestGateway(t, gate)
	conn := newTestConnection("conn-a")
	require.NoError(t, gate.Open(context.Background(), conn))
	_, err := gate.Handle(context.Background(), conn, authMessage(t))
	require.NoError(t, err)
	bindTestPlayerNode(t, store, "game", "player-a", "node-b")

	request := &protocolv1.Proto{Op: protocolv1.OpRequest, Cmd: 1}
	require.Eventually(t, func() bool {
		request = &protocolv1.Proto{Op: protocolv1.OpRequest, Cmd: 1}
		_, err = gate.Handle(context.Background(), conn, request)
		return err == nil && request.Code == int32(codes.OK)
	}, time.Second, time.Millisecond)
	require.Equal(t, []byte("node-b:node-b"), request.Body)

	bindTestPlayerNode(t, store, "game", "player-a", "missing")
	request = &protocolv1.Proto{Op: protocolv1.OpRequest, Cmd: 1}
	_, err = gate.Handle(context.Background(), conn, request)
	require.NoError(t, err)
	require.Equal(t, int32(codes.Unavailable), request.Code)
}

func TestGatewayStatelessServiceSkipsNodeLookup(t *testing.T) {
	store := &nodeLookupCountingLocator{Locator: testLocator(t)}
	endpoint := startNamedBackendNode(t, "node-a")
	registered := serviceInstance("game", "node-a", endpoint)
	registered.Metadata = nil
	gate := newTestServer(t,
		Locator(store),
		Discovery(staticDiscovery{"game": {registered}}),
	)
	initTestGateway(t, gate)
	conn := newTestConnection("conn-a")
	require.NoError(t, gate.Open(context.Background(), conn))
	_, err := gate.Handle(context.Background(), conn, authMessage(t))
	require.NoError(t, err)

	request := &protocolv1.Proto{Op: protocolv1.OpRequest, Cmd: 1}
	_, err = gate.Handle(context.Background(), conn, request)
	require.NoError(t, err)
	require.Equal(t, int32(codes.OK), request.Code)
	require.Equal(t, []byte("node-a:"), request.Body)
	require.Zero(t, store.nodeLookups.Load())
}

func TestSessionRoute(t *testing.T) {
	now := time.Now()
	binding := testBinding()
	tests := []struct {
		name     string
		binding  locate.GateBinding
		deadline time.Time
		want     bool
	}{
		{name: "current lease", binding: binding, deadline: now.Add(time.Minute), want: true},
		{name: "expired lease", binding: binding, deadline: now.Add(-time.Millisecond)},
		{name: "missing binding", deadline: now.Add(time.Minute)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sess := &session{
				conn:          newTestConnection(binding.ConnID),
				binding:       test.binding,
				leaseDeadline: test.deadline,
			}

			got, valid := sess.route(now)

			require.Equal(t, test.binding, got)
			require.Equal(t, test.want, valid)
		})
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

type epochCountingLocator struct {
	locate.Locator
	epochLookups atomic.Int32
}

func (l *epochCountingLocator) LocateNodeEpoch(ctx context.Context, serviceName, nodeID string) (string, error) {
	l.epochLookups.Add(1)
	return l.Locator.LocateNodeEpoch(ctx, serviceName, nodeID)
}

type failingEpochLocator struct {
	locate.Locator
	err error
}

func (l *failingEpochLocator) LocateNodeEpoch(context.Context, string, string) (string, error) {
	return "", l.err
}

func newForwardTestGateway(t *testing.T, service *forwardServer) *Server {
	t.Helper()
	endpoint := startBackendNode(t, service)
	store := testLocator(t)
	backendPool := newBackends(staticDiscovery{"game": {
		serviceInstance("game", "node-a", endpoint),
	}}, nil, time.Second, store)
	t.Cleanup(backendPool.close)
	return &Server{backends: backendPool, rpcTimeout: time.Second}
}

type blockingNodeLocator struct {
	locate.Locator
}

func (l *blockingNodeLocator) LocateNode(ctx context.Context, _, _ string) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

type nodeLookupCountingLocator struct {
	locate.Locator
	nodeLookups atomic.Int32
}

func (l *nodeLookupCountingLocator) LocateNode(ctx context.Context, serviceName, uid string) (string, error) {
	l.nodeLookups.Add(1)
	return l.Locator.LocateNode(ctx, serviceName, uid)
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
