package node

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"yola/api/cluster/v1"
	"yola/internal/clusterroute"

	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/transport"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestRegisterAppliesMiddlewareToTypedRequest(t *testing.T) {
	calls := make([]string, 0, 3)
	server := newDispatchTestServer(t, Middleware(func(next middleware.Handler) middleware.Handler {
		return func(ctx context.Context, request any) (any, error) {
			calls = append(calls, "before")
			req, requestOK := request.(*wrapperspb.StringValue)
			require.True(t, requestOK)
			sess, ok := FromContext(ctx)
			require.True(t, ok)
			reply, err := next(ctx, wrapperspb.String(sess.UID()+":"+req.Value))
			calls = append(calls, "after")
			return reply, err
		}
	}))
	t.Cleanup(func() { require.NoError(t, server.Stop(context.Background())) })
	Register(server, int32(1), func(_ context.Context, request *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
		calls = append(calls, "handler")
		return wrapperspb.String("reply:" + request.Value), nil
	})

	request, err := proto.Marshal(wrapperspb.String("request"))
	require.NoError(t, err)
	body, err := server.forward(context.Background(), testBinding("player-a", "conn-a"), 1, request)
	require.NoError(t, err)
	reply := &wrapperspb.StringValue{}
	require.NoError(t, proto.Unmarshal(body, reply))
	require.Equal(t, "reply:player-a:request", reply.Value)
	require.Equal(t, []string{"before", "handler", "after"}, calls)

	_, err = server.forward(context.Background(), testBinding("player-a", "conn-a"), 1, []byte{0xff})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Equal(t, []string{"before", "handler", "after"}, calls)
}

func TestRegisterHandlesTypedHandlerErrors(t *testing.T) {
	handlerErr := status.Error(codes.Aborted, "rejected")
	server := newDispatchTestServer(t)
	t.Cleanup(func() { require.NoError(t, server.Stop(context.Background())) })
	Register(server, int32(2), func(context.Context, *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
		return nil, handlerErr
	})
	Register(server, int32(3), func(context.Context, *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
		return nil, nil
	})
	Register(server, int32(4), func(context.Context, *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
		return wrapperspb.String("\xff"), nil
	})
	binding := testBinding("player-a", "conn-a")

	_, err := server.forward(context.Background(), binding, 2, nil)
	require.ErrorIs(t, err, handlerErr)
	_, err = server.forward(context.Background(), binding, 3, nil)
	require.Equal(t, codes.Internal, status.Code(err))
	_, err = server.forward(context.Background(), binding, 4, nil)
	require.Equal(t, codes.Internal, status.Code(err))
}

func TestRegisterRejectsInvalidConfiguration(t *testing.T) {
	server := newTestServer(t)
	t.Cleanup(func() { require.NoError(t, server.Stop(context.Background())) })
	require.Panics(t, func() {
		Register[int32, wrapperspb.StringValue, *wrapperspb.StringValue, *wrapperspb.StringValue](nil, 1, nil)
	})
	require.Panics(t, func() {
		Register[int32, wrapperspb.StringValue, *wrapperspb.StringValue, *wrapperspb.StringValue](server, 2, nil)
	})
	require.Panics(t, func() { server.RegisterRawHandler(1, nil) })
	server.RegisterRawHandler(1, func(context.Context, []byte) ([]byte, error) {
		return nil, nil
	})
	require.Panics(t, func() {
		server.RegisterRawHandler(1, func(context.Context, []byte) ([]byte, error) {
			return nil, nil
		})
	})
}

func TestForwardAllowsSameUIDConcurrently(t *testing.T) {
	entered := make(chan string, 2)
	release := make(chan struct{})
	server := newDispatchTestServer(t)
	t.Cleanup(func() { require.NoError(t, server.Stop(context.Background())) })
	server.RegisterRawHandler(1, func(ctx context.Context, body []byte) ([]byte, error) {
		sess, ok := FromContext(ctx)
		if !ok {
			return nil, status.Error(codes.Internal, "session is missing")
		}
		if sess.UID() != "player-a" {
			return nil, status.Error(codes.Internal, "unexpected session UID")
		}
		entered <- string(body)
		<-release
		return nil, nil
	})

	var wg sync.WaitGroup
	for _, body := range []string{"first", "second"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = server.forward(
				context.Background(), testBinding("player-a", "conn-a"), 1, []byte(body),
			)
		}()
	}
	seen := make([]string, 0, 2)
	for range 2 {
		select {
		case body := <-entered:
			seen = append(seen, body)
		case <-time.After(time.Second):
			close(release)
			t.Fatal("same-UID requests did not run concurrently")
		}
	}
	require.ElementsMatch(t, []string{"first", "second"}, seen)
	close(release)
	wg.Wait()
}

func TestForwardToRejectsWrongEpoch(t *testing.T) {
	server := newTestServer(t, Stateful(newMemoryNodeStore()))
	const epoch = "epoch-a"
	publishTestIdentity(server, nodeIdentity{serviceName: "game", nodeID: "node-a", epoch: epoch})
	binding := testBinding("player-a", "conn-a")

	_, err := server.forwardTo(context.Background(), stickyClaim{NodeID: "node-a", Epoch: "wrong-epoch"}, binding, 1, nil)
	require.Equal(t, codes.Aborted, status.Code(err))
	require.Contains(t, err.Error(), "node epoch mismatch")

	_, err = server.forwardTo(context.Background(), stickyClaim{NodeID: "node-a"}, binding, 1, nil)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, err.Error(), "invalid node claim")
}

func TestClusterForward(t *testing.T) {
	handlerErr := status.Error(codes.Aborted, "rejected")
	server := newDispatchTestServer(t)
	t.Cleanup(func() { require.NoError(t, server.Stop(context.Background())) })
	server.RegisterRawHandler(1, func(ctx context.Context, body []byte) ([]byte, error) {
		sess, _ := FromContext(ctx)
		return append([]byte(sess.UID()+":"), body...), nil
	})
	server.RegisterRawHandler(2, func(context.Context, []byte) ([]byte, error) {
		return nil, handlerErr
	})
	Register(server, int32(3), func(context.Context, *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
		return wrapperspb.String("ok"), nil
	})
	binding := testBinding("player-a", "conn-a")
	service := &forwardService{server: server}

	reply, err := service.Forward(context.Background(), &v1.ForwardRequest{
		Route:   clusterroute.FromBinding(binding),
		Command: 1,
		Body:    []byte("request"),
	})
	require.NoError(t, err)
	require.Equal(t, []byte("player-a:request"), reply.Body)

	invalid := binding
	invalid.BindingToken = ""
	_, err = service.Forward(context.Background(), &v1.ForwardRequest{Route: clusterroute.FromBinding(invalid), Command: 1})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = service.Forward(context.Background(), &v1.ForwardRequest{Route: clusterroute.FromBinding(binding), Command: 99})
	require.Equal(t, codes.Unimplemented, status.Code(err))

	_, err = service.Forward(context.Background(), &v1.ForwardRequest{
		Route:   clusterroute.FromBinding(binding),
		Command: 2,
	})
	require.ErrorIs(t, err, handlerErr)
	require.Equal(t, codes.Aborted, status.Code(err))

	_, err = service.Forward(context.Background(), &v1.ForwardRequest{
		Route:   clusterroute.FromBinding(binding),
		Command: 3,
		Body:    []byte{0xff},
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	_, err = service.Forward(context.Background(), &v1.ForwardRequest{
		Route:     clusterroute.FromBinding(binding),
		Command:   1,
		NodeId:    "another-node",
		NodeEpoch: "epoch-a",
	})
	require.Equal(t, codes.Aborted, status.Code(err))

	wrongService := binding
	wrongService.ServiceName = "chat"
	_, err = service.Forward(context.Background(), &v1.ForwardRequest{
		Route:   clusterroute.FromBinding(wrongService),
		Command: 1,
	})
	require.Equal(t, codes.Aborted, status.Code(err))

	_, err = service.Forward(context.Background(), &v1.ForwardRequest{
		Route:     clusterroute.FromBinding(binding),
		Command:   1,
		NodeEpoch: "orphan-epoch",
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestDisconnectHandlerReceivesSession(t *testing.T) {
	server := newDispatchTestServer(t)
	t.Cleanup(func() { require.NoError(t, server.Stop(context.Background())) })
	got := make(chan string, 1)
	server.OnDisconnect(func(ctx context.Context, sess Session) error {
		require.NotNil(t, sess)
		fromCtx, ok := FromContext(ctx)
		require.True(t, ok)
		require.Equal(t, sess.UID(), fromCtx.UID())
		require.Equal(t, "binding-player-a", sess.BindingToken())
		got <- sess.UID()
		return nil
	})

	svc := &forwardService{server: server}
	_, err := svc.Disconnect(context.Background(), &v1.DisconnectRequest{
		Route: clusterroute.FromBinding(testBinding("player-a", "conn-a")),
	})
	require.NoError(t, err)
	require.Equal(t, "player-a", <-got)

	wrongService := testBinding("player-a", "conn-a")
	wrongService.ServiceName = "chat"
	_, err = svc.Disconnect(context.Background(), &v1.DisconnectRequest{Route: clusterroute.FromBinding(wrongService)})
	require.Equal(t, codes.Aborted, status.Code(err))
	require.Empty(t, got)

	server.OnDisconnect(nil)
	_, err = svc.Disconnect(context.Background(), &v1.DisconnectRequest{
		Route: clusterroute.FromBinding(testBinding("player-b", "conn-b")),
	})
	require.NoError(t, err)
}

func TestCommandMetadataDistinguishesSharedRequestsThroughGRPC(t *testing.T) {
	type observation struct {
		stage     string
		command   int32
		present   bool
		uid       string
		operation string
		typed     bool
		err       error
	}
	observed := make(chan observation, 16)
	record := func(ctx context.Context, stage string, request any, err error) {
		command, present := CommandFromContext(ctx)
		var uid, operation string
		if sess, ok := FromContext(ctx); ok {
			uid = sess.UID()
		}
		if tr, ok := transport.FromServerContext(ctx); ok {
			operation = tr.Operation()
		}
		_, typed := request.(*emptypb.Empty)
		observed <- observation{stage: stage, command: command, present: present, uid: uid, operation: operation, typed: typed, err: err}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := newTestServer(t, Listener(listener), Middleware(func(next middleware.Handler) middleware.Handler {
		return func(ctx context.Context, request any) (any, error) {
			record(ctx, "before", request, nil)
			reply, handlerErr := next(ctx, request)
			record(ctx, "after", request, handlerErr)
			return reply, handlerErr
		}
	}))
	cause := status.Error(codes.Aborted, "command rejected")
	for _, command := range []int32{11, 22} {
		Register(server, command, func(ctx context.Context, request *emptypb.Empty) (*emptypb.Empty, error) {
			record(ctx, "handler", request, nil)
			if command == 22 {
				return nil, cause
			}
			return &emptypb.Empty{}, nil
		})
	}
	server.RegisterRawHandler(0, func(ctx context.Context, body []byte) ([]byte, error) {
		record(ctx, "raw", body, nil)
		return body, nil
	})
	appCtx := testNodeContext()

	done := make(chan error, 1)
	go func() { done <- server.Start(appCtx) }()
	t.Cleanup(func() {
		require.NoError(t, server.Stop(context.Background()))
		require.NoError(t, <-done)
	})
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	client := v1.NewNodeClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for _, command := range []int32{11, 22} {
		_, callErr := client.Forward(ctx, &v1.ForwardRequest{
			Route:   clusterroute.FromBinding(testBinding("player-a", "conn-a")),
			Command: command,
		})
		if command == 22 {
			require.Equal(t, codes.Aborted, status.Code(callErr))
		} else {
			require.NoError(t, callErr)
		}
		for _, stage := range []string{"before", "handler", "after"} {
			seen := receiveNodeValue(t, observed)
			require.Equal(t, stage, seen.stage)
			require.True(t, seen.present)
			require.Equal(t, command, seen.command)
			require.Equal(t, "player-a", seen.uid)
			require.Equal(t, v1.Node_Forward_FullMethodName, seen.operation)
			require.True(t, seen.typed)
			if command == 22 && stage == "after" {
				require.ErrorIs(t, seen.err, cause)
			}
		}
	}
	_, err = client.Forward(ctx, &v1.ForwardRequest{Route: clusterroute.FromBinding(testBinding("player-a", "conn-a"))})
	require.NoError(t, err)
	seen := receiveNodeValue(t, observed)
	require.Equal(t, "raw", seen.stage, "raw handlers must not automatically enter typed middleware")
	require.True(t, seen.present)
	require.Zero(t, seen.command)
	select {
	case extra := <-observed:
		t.Fatalf("unexpected middleware call for raw handler: %+v", extra)
	default:
	}
}

func TestCommandMetadataIsAbsentOutsideForward(t *testing.T) {
	for _, ctx := range []context.Context{nil, context.Background()} {
		command, ok := CommandFromContext(ctx)
		require.False(t, ok)
		require.Zero(t, command)
	}
}
