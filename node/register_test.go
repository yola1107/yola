package node

import (
	"context"
	"testing"

	"github.com/go-kratos/kratos/v3"
	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestRegisterAppliesMiddlewareToTypedRequest(t *testing.T) {
	calls := make([]string, 0, 3)
	server := newDispatchTestServer(t, Middleware(func(next middleware.Handler) middleware.Handler {
		return func(ctx context.Context, request any) (any, error) {
			calls = append(calls, "before")
			req := request.(*wrapperspb.StringValue)
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
	reply := new(wrapperspb.StringValue)
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
	Register(server, int32(1), func(context.Context, *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
		return wrapperspb.String("reply"), nil
	})
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

	_, err := server.forward(context.Background(), binding, 1, []byte{0xff})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = server.forward(context.Background(), binding, 2, nil)
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

func TestHandlerRegistrationRejectsAfterBeforeStart(t *testing.T) {
	server := newTestServer(t)
	t.Cleanup(func() { require.NoError(t, server.Stop(context.Background())) })
	ctx := kratos.NewContext(context.Background(), nodeTestAppInfo{})
	require.NoError(t, server.BeforeStart(ctx))

	assertHandlerRegistrationFrozen(t, server)
}
