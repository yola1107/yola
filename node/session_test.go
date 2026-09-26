package node

import (
	"context"
	"net"
	"testing"
	"time"

	v1 "yola/api/cluster/v1"
	"yola/internal/clusterroute"

	"github.com/go-kratos/kratos/v3"
	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/transport"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestSessionBindsNodeAndStatefulForwardRevalidates(t *testing.T) {
	locator := newMemoryLocator()
	server := newTestServer(t, Locator(locator))
	const epoch = "epoch-a"
	publishTestIdentity(server, nodeIdentity{serviceName: "game", nodeID: "node-a", epoch: epoch})
	require.NoError(t, locator.RegisterNodeEpoch(context.Background(), "game", "node-a", epoch, DefaultNodeEpochTTL))
	server.RegisterRawHandler(1, func(ctx context.Context, _ []byte) ([]byte, error) {
		sess, _ := FromContext(ctx)
		return []byte("bound"), sess.BindNode(ctx)
	})
	server.RegisterRawHandler(2, func(context.Context, []byte) ([]byte, error) {
		return []byte("stateful"), nil
	})
	binding := testBinding("player-a", "conn-a")

	body, err := server.forward(context.Background(), binding, 1, nil)
	require.NoError(t, err)
	require.Equal(t, []byte("bound"), body)
	located, err := locator.LocateNode(context.Background(), "game", "player-a")
	require.NoError(t, err)
	require.Equal(t, "node-a", located)

	body, err = server.forwardTo(context.Background(), stickyClaim{NodeID: "node-a", Epoch: epoch}, binding, 2, nil)
	require.NoError(t, err)
	require.Equal(t, []byte("stateful"), body)
	require.NoError(t, locator.RegisterNodeEpoch(context.Background(), "game", "node-b", "epoch-b", DefaultNodeEpochTTL))
	require.NoError(t, locator.BindNode(context.Background(), "game", "player-a", "node-b", "epoch-b"))
	_, err = server.forwardTo(context.Background(), stickyClaim{NodeID: "node-a", Epoch: epoch}, binding, 2, nil)
	require.Equal(t, codes.Aborted, status.Code(err))
}

func TestSessionUnbindDoesNotDeleteNewNode(t *testing.T) {
	locator := newMemoryLocator()
	server := newTestServer(t, Locator(locator))
	publishTestIdentity(server, nodeIdentity{serviceName: "game", nodeID: "node-a", epoch: "epoch-a"})
	require.NoError(t, locator.RegisterNodeEpoch(context.Background(), "game", "node-a", "epoch-a", DefaultNodeEpochTTL))
	server.RegisterRawHandler(1, func(ctx context.Context, _ []byte) ([]byte, error) {
		sess, _ := FromContext(ctx)
		return nil, sess.UnbindNode(ctx)
	})
	require.NoError(t, locator.RegisterNodeEpoch(context.Background(), "game", "node-b", "epoch-b", DefaultNodeEpochTTL))
	require.NoError(t, locator.BindNode(context.Background(), "game", "player-a", "node-b", "epoch-b"))

	_, err := server.forward(context.Background(), testBinding("player-a", "conn-a"), 1, nil)
	require.NoError(t, err)
	located, err := locator.LocateNode(context.Background(), "game", "player-a")
	require.NoError(t, err)
	require.Equal(t, "node-b", located)
}

func TestSessionNodeBindingRequiresLocator(t *testing.T) {
	server := newTestServer(t)
	publishTestIdentity(server, nodeIdentity{serviceName: "game", nodeID: "node-a"})
	sess := requestSession{binding: testBinding("player-a", "conn-a"), server: server}

	require.Equal(t, codes.FailedPrecondition, status.Code(sess.BindNode(context.Background())))
	require.Equal(t, codes.FailedPrecondition, status.Code(sess.UnbindNode(context.Background())))
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
			return new(emptypb.Empty), nil
		})
	}
	server.RegisterRawHandler(0, func(ctx context.Context, body []byte) ([]byte, error) {
		record(ctx, "raw", body, nil)
		return body, nil
	})
	appCtx := kratos.NewContext(context.Background(), nodeTestAppInfo{})
	require.NoError(t, server.BeforeStart(appCtx))
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
			Route: clusterroute.FromBinding(testBinding("player-a", "conn-a")), Command: command,
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
