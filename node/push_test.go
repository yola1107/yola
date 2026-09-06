package node

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"yola/api/cluster/v1"
	"yola/locate"

	kgrpc "github.com/go-kratos/kratos/v3/transport/grpc"
	"github.com/stretchr/testify/require"
	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

type gatewayStub struct {
	v1.UnimplementedGatewayServer
	pushes chan *v1.PushRequest
	push   func(context.Context, *v1.PushRequest) (*emptypb.Empty, error)
}

func (s *gatewayStub) Push(ctx context.Context, in *v1.PushRequest) (*emptypb.Empty, error) {
	if s.push != nil {
		return s.push(ctx, in)
	}
	s.pushes <- in
	return &emptypb.Empty{}, nil
}

func startGatewayStub(t *testing.T, stub *gatewayStub) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	gate := kgrpc.NewServer(kgrpc.Listener(lis))
	v1.RegisterGatewayServer(gate, stub)
	done := make(chan error, 1)
	go func() { done <- gate.Start(context.Background()) }()
	t.Cleanup(func() {
		require.NoError(t, gate.Stop(context.Background()))
		require.NoError(t, receiveNodeValue(t, done))
	})
	return lis.Addr().String()
}

type blockingGateLocator struct {
	locate.Locator
}

func (*blockingGateLocator) LocateGate(ctx context.Context, _, _ string) (locate.GateLease, error) {
	<-ctx.Done()
	return locate.GateLease{}, ctx.Err()
}

func TestSessionPushAndClose(t *testing.T) {
	stub := &gatewayStub{pushes: make(chan *v1.PushRequest, 1)}
	gateAddress := startGatewayStub(t, stub)

	server := newDispatchTestServer(t, PushTimeout(time.Second))
	server.RegisterRawHandler(1, func(ctx context.Context, _ []byte) ([]byte, error) {
		sess, _ := FromContext(ctx)
		return []byte("ok"), sess.Push(ctx, 2, wrapperspb.String("push"))
	})
	binding := testBinding("player-a", "conn-a")
	binding.GateEndpoint = "grpc://" + gateAddress

	body, err := server.forward(context.Background(), binding, 1, nil)
	require.NoError(t, err)
	require.Equal(t, []byte("ok"), body)
	push := <-stub.pushes
	want, err := proto.Marshal(wrapperspb.String("push"))
	require.NoError(t, err)
	require.Equal(t, want, push.GetBody())
	require.Equal(t, binding.BindingToken, push.GetRoute().GetBindingToken())

	require.NoError(t, server.Stop(context.Background()))
	require.NoError(t, server.Stop(context.Background()))
	_, err = server.forward(context.Background(), binding, 1, nil)
	requireNodeDraining(t, err)
}

func TestSessionPushUsesConfiguredTimeout(t *testing.T) {
	var enteredOnce sync.Once
	entered := make(chan struct{})
	stub := &gatewayStub{push: func(ctx context.Context, _ *v1.PushRequest) (*emptypb.Empty, error) {
		enteredOnce.Do(func() { close(entered) })
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	gateAddress := startGatewayStub(t, stub)
	// Listener 已打开时 TCP dial 会立刻成功，必须等到 gRPC Serve 就绪，否则短 PushTimeout 会在 dial 阶段假超时。
	require.Eventually(t, func() bool {
		conn, dialErr := grpcgo.NewClient(gateAddress, grpcgo.WithTransportCredentials(insecure.NewCredentials()))
		if dialErr != nil {
			return false
		}
		defer conn.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		conn.Connect()
		for {
			state := conn.GetState()
			if state == connectivity.Ready {
				return true
			}
			if !conn.WaitForStateChange(ctx, state) {
				return false
			}
		}
	}, 2*time.Second, 10*time.Millisecond)

	server := newDispatchTestServer(t, PushTimeout(200*time.Millisecond))
	t.Cleanup(func() { require.NoError(t, server.Stop(context.Background())) })
	server.RegisterRawHandler(1, func(ctx context.Context, _ []byte) ([]byte, error) {
		sess, _ := FromContext(ctx)
		return nil, sess.Push(context.Background(), 2, wrapperspb.String("push"))
	})
	binding := testBinding("player-a", "conn-a")
	binding.GateEndpoint = "grpc://" + gateAddress

	started := time.Now()
	_, err := server.forward(context.Background(), binding, 1, nil)
	require.Equal(t, codes.DeadlineExceeded, status.Code(err))
	require.Less(t, time.Since(started), time.Second)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("push did not reach gateway stub")
	}
}

func TestAcceptedSessionPushCompletesWhileStopDrains(t *testing.T) {
	stub := &gatewayStub{pushes: make(chan *v1.PushRequest, 1)}
	gateAddress := startGatewayStub(t, stub)

	server := newDispatchTestServer(t, PushTimeout(time.Second))
	entered := make(chan struct{})
	release := make(chan struct{})
	releaseHandler := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseHandler)
	server.RegisterRawHandler(1, func(ctx context.Context, _ []byte) ([]byte, error) {
		close(entered)
		<-release
		sess, _ := FromContext(ctx)
		return nil, sess.Push(ctx, 2, wrapperspb.String("push"))
	})
	binding := testBinding("player-a", "conn-a")
	binding.GateEndpoint = "grpc://" + gateAddress

	forwardDone := make(chan error, 1)
	go func() {
		_, err := server.forward(context.Background(), binding, 1, nil)
		forwardDone <- err
	}()
	waitNodeSignal(t, entered)
	stopDone := make(chan error, 1)
	go func() { stopDone <- server.Stop(context.Background()) }()
	require.Eventually(t, server.requests.isClosed, time.Second, time.Millisecond)
	releaseHandler()

	require.NoError(t, receiveNodeValue(t, forwardDone))
	require.NotNil(t, receiveNodeValue(t, stub.pushes))
	require.NoError(t, receiveNodeValue(t, stopDone))
}

func TestAcceptedHandlerPushToUIDCompletesWhileStopDrains(t *testing.T) {
	stub := &gatewayStub{pushes: make(chan *v1.PushRequest, 1)}
	binding := testBinding("player-a", "conn-a")
	binding.GateEndpoint = "grpc://" + startGatewayStub(t, stub)
	store := newMemoryLocator()
	_, _, err := store.BindGate(context.Background(), binding, time.Minute)
	require.NoError(t, err)
	server := newDispatchTestServer(t, Locator(store), PushTimeout(time.Second))
	t.Cleanup(func() { require.NoError(t, server.Stop(context.Background())) })
	entered := make(chan struct{})
	release := make(chan struct{})
	releaseHandler := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseHandler)
	server.RegisterRawHandler(1, func(ctx context.Context, _ []byte) ([]byte, error) {
		close(entered)
		<-release
		return nil, server.PushToUID(ctx, binding.UID, 2, wrapperspb.String("push"))
	})
	forwardDone := make(chan error, 1)
	go func() {
		_, forwardErr := server.forward(context.Background(), binding, 1, nil)
		forwardDone <- forwardErr
	}()
	waitNodeSignal(t, entered)
	stopDone := make(chan error, 1)
	go func() { stopDone <- server.Stop(context.Background()) }()
	require.Eventually(t, server.requests.isClosed, time.Second, time.Millisecond)
	releaseHandler()

	require.NoError(t, receiveNodeValue(t, forwardDone))
	require.NotNil(t, receiveNodeValue(t, stub.pushes))
	require.NoError(t, receiveNodeValue(t, stopDone))
}

func TestBusinessDrainCanPushToUID(t *testing.T) {
	stub := &gatewayStub{pushes: make(chan *v1.PushRequest, 1)}
	binding := testBinding("player-a", "conn-a")
	binding.GateEndpoint = "grpc://" + startGatewayStub(t, stub)
	store := newMemoryLocator()
	_, _, err := store.BindGate(context.Background(), binding, time.Minute)
	require.NoError(t, err)
	var server *Server
	server = newDispatchTestServer(t, Locator(store), Drain(func(ctx context.Context) error {
		return server.PushToUID(ctx, binding.UID, 2, wrapperspb.String("drain"))
	}))

	require.NoError(t, server.Stop(context.Background()))
	require.NotNil(t, receiveNodeValue(t, stub.pushes))
	require.Equal(t, codes.Unavailable, status.Code(server.PushToUID(context.Background(), binding.UID, 2, wrapperspb.String("closed"))))
}

func TestStopWaitsForDeliveriesBeforeReleasingEpoch(t *testing.T) {
	for _, method := range []string{"PushToUID", "Session.Push"} {
		t.Run(method, func(t *testing.T) {
			entered := make(chan struct{})
			release := make(chan struct{})
			releasePush := sync.OnceFunc(func() { close(release) })
			stub := &gatewayStub{push: func(context.Context, *v1.PushRequest) (*emptypb.Empty, error) {
				close(entered)
				<-release
				return &emptypb.Empty{}, nil
			}}
			binding := testBinding("player-a", "conn-a")
			binding.GateEndpoint = "grpc://" + startGatewayStub(t, stub)
			store := newMemoryLocator()
			_, _, err := store.BindGate(context.Background(), binding, time.Minute)
			require.NoError(t, err)
			require.NoError(t, store.RegisterNodeEpoch(context.Background(), "game", "node-a", "epoch-a", DefaultNodeEpochTTL))
			server := newDispatchTestServer(t, Locator(store))
			publishTestIdentity(server, nodeIdentity{serviceName: "game", nodeID: "node-a", epoch: "epoch-a"})
			t.Cleanup(func() { require.NoError(t, server.Stop(context.Background())) })
			t.Cleanup(releasePush)
			pushed := make(chan error, 1)
			go func() {
				if method == "PushToUID" {
					pushed <- server.PushToUID(context.Background(), binding.UID, 2, wrapperspb.String("push"))
					return
				}
				pushed <- (requestSession{server: server, binding: binding}).Push(context.Background(), 2, wrapperspb.String("push"))
			}()
			waitNodeSignal(t, entered)
			stopped := make(chan error, 1)
			go func() { stopped <- server.Stop(context.Background()) }()
			require.Eventually(t, server.deliveries.isClosed, time.Second, time.Millisecond)
			epoch, err := store.LocateNodeEpoch(context.Background(), "game", "node-a")
			require.NoError(t, err)
			require.Equal(t, "epoch-a", epoch)
			select {
			case stopErr := <-stopped:
				t.Fatalf("Stop returned with active delivery: %v", stopErr)
			default:
			}
			releasePush()
			require.NoError(t, receiveNodeValue(t, pushed))
			require.NoError(t, receiveNodeValue(t, stopped))
			_, err = store.LocateNodeEpoch(context.Background(), "game", "node-a")
			require.ErrorIs(t, err, locate.ErrNodeEpochNotFound)
		})
	}
}

func TestDeliveryDrainTimeoutKeepsEpochUntilTTL(t *testing.T) {
	entered := make(chan struct{})
	stub := &gatewayStub{push: func(ctx context.Context, _ *v1.PushRequest) (*emptypb.Empty, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	binding := testBinding("player-a", "conn-a")
	binding.GateEndpoint = "grpc://" + startGatewayStub(t, stub)
	store := newMemoryLocator()
	_, _, err := store.BindGate(context.Background(), binding, time.Minute)
	require.NoError(t, err)
	require.NoError(t, store.RegisterNodeEpoch(context.Background(), "game", "node-a", "epoch-a", DefaultNodeEpochTTL))
	server := newDispatchTestServer(t, Locator(store))
	publishTestIdentity(server, nodeIdentity{serviceName: "game", nodeID: "node-a", epoch: "epoch-a"})
	// Stop 可能已记录预期的排空超时，清理不重复断言它的返回值。
	t.Cleanup(func() { _ = server.Stop(context.Background()) })
	pushed := make(chan error, 1)
	go func() { pushed <- server.PushToUID(context.Background(), binding.UID, 2, wrapperspb.String("push")) }()
	waitNodeSignal(t, entered)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, server.Stop(ctx), context.DeadlineExceeded)
	require.Error(t, receiveNodeValue(t, pushed))
	require.ErrorIs(t, server.Stop(context.Background()), context.DeadlineExceeded)
	epoch, err := store.LocateNodeEpoch(context.Background(), "game", "node-a")
	require.NoError(t, err)
	require.Equal(t, "epoch-a", epoch)
}

func TestPushToUIDLocatesGateAndPushes(t *testing.T) {
	stub := &gatewayStub{pushes: make(chan *v1.PushRequest, 1)}
	gateAddress := startGatewayStub(t, stub)

	binding := testBinding("player-a", "conn-a")
	binding.GateEndpoint = "grpc://" + gateAddress
	locator := newMemoryLocator()
	require.NoError(t, locator.BindNode(context.Background(), "game", "player-a", "node-a"))
	_, _, err := locator.BindGate(context.Background(), binding, time.Minute)
	require.NoError(t, err)

	server := newTestServer(t, Locator(locator), PushTimeout(time.Second))
	publishTestIdentity(server, nodeIdentity{serviceName: "game", nodeID: "node-a"})
	t.Cleanup(func() { require.NoError(t, server.Stop(context.Background())) })

	require.NoError(t, server.PushToUID(context.Background(), "player-a", 2, wrapperspb.String("push")))
	push := <-stub.pushes
	require.Equal(t, binding.BindingToken, push.GetRoute().GetBindingToken())
	require.Equal(t, int32(2), push.GetCommand())
}

func TestPushToUIDTimeoutIncludesGateLookup(t *testing.T) {
	locator := &blockingGateLocator{Locator: newMemoryLocator()}
	server := newTestServer(t, Locator(locator), PushTimeout(20*time.Millisecond))
	publishTestIdentity(server, nodeIdentity{serviceName: "game", nodeID: "node-a"})
	t.Cleanup(func() { require.NoError(t, server.Stop(context.Background())) })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	started := time.Now()
	err := server.PushToUID(ctx, "player-a", 2, wrapperspb.String("push"))
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(started), 200*time.Millisecond)
}

func TestPushToUIDRejectsAfterStop(t *testing.T) {
	server := newTestServer(t, Locator(newMemoryLocator()))
	publishTestIdentity(server, nodeIdentity{serviceName: "game", nodeID: "node-a"})
	require.NoError(t, server.Stop(context.Background()))

	err := server.PushToUID(context.Background(), "player-a", 1, wrapperspb.String("push"))
	require.Equal(t, codes.Unavailable, status.Code(err))
	require.Equal(t, "node is stopping or stopped", status.Convert(err).Message())
}

func TestSessionPushMapsGatewayInvariantErrors(t *testing.T) {
	for _, test := range []struct {
		name     string
		binding  func() locate.GateBinding
		close    bool
		wantCode codes.Code
		wantMsg  string
	}{
		{name: "invalid route", binding: func() locate.GateBinding {
			binding := testBinding("player-a", "")
			return binding
		}, wantCode: codes.Internal, wantMsg: "invalid gateway route"},
		{name: "invalid endpoint", binding: func() locate.GateBinding {
			binding := testBinding("player-a", "conn-a")
			binding.GateEndpoint = "not-an-endpoint"
			return binding
		}, wantCode: codes.Internal, wantMsg: "invalid gateway route"},
		{name: "transport mismatch", binding: func() locate.GateBinding {
			binding := testBinding("player-a", "conn-a")
			binding.GateEndpoint = "grpcs://127.0.0.1:9000"
			return binding
		}, wantCode: codes.Internal, wantMsg: "invalid gateway route"},
		{name: "closed client", binding: func() locate.GateBinding {
			return testBinding("player-a", "conn-a")
		}, close: true, wantCode: codes.Unavailable, wantMsg: "gateway is unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := newDispatchTestServer(t)
			t.Cleanup(func() { _ = server.Stop(context.Background()) })
			if test.close {
				require.NoError(t, server.gateways.Close())
			}
			sess := requestSession{binding: test.binding(), server: server}

			err := sess.Push(context.Background(), 1, wrapperspb.String("push"))
			require.Equal(t, test.wantCode, status.Code(err))
			require.Equal(t, test.wantMsg, status.Convert(err).Message())
		})
	}
}

func TestMapGatePushErrorPreservesContextAndStatus(t *testing.T) {
	rpcErr := status.Error(codes.Aborted, "binding changed")
	for _, err := range []error{context.Canceled, context.DeadlineExceeded, rpcErr} {
		require.Equal(t, err, mapGatePushError(err))
	}
}

func TestPushRejectsInvalidMessage(t *testing.T) {
	server := newDispatchTestServer(t, Locator(newMemoryLocator()))
	t.Cleanup(func() { require.NoError(t, server.Stop(context.Background())) })
	sess := requestSession{binding: testBinding("player-a", "conn-a"), server: server}
	var typedNil *wrapperspb.StringValue

	for _, test := range []struct {
		name string
		msg  proto.Message
	}{
		{name: "nil"},
		{name: "typed nil", msg: typedNil},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := sess.Push(context.Background(), 1, test.msg)
			require.Equal(t, codes.InvalidArgument, status.Code(err))
			err = server.PushToUID(context.Background(), "player-a", 1, test.msg)
			require.Equal(t, codes.InvalidArgument, status.Code(err))
		})
	}
}
