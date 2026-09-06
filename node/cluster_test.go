package node

import (
	"context"
	"testing"

	"yola/api/cluster/v1"
	"yola/internal/clusterroute"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

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
		Route: clusterroute.FromBinding(binding), Command: 1, Body: []byte("request"),
	})
	require.NoError(t, err)
	require.Equal(t, []byte("player-a:request"), reply.Body)

	_, err = service.Forward(context.Background(), &v1.ForwardRequest{
		Route: clusterroute.FromBinding(binding), Command: 2,
	})
	require.ErrorIs(t, err, handlerErr)
	require.Equal(t, codes.Aborted, status.Code(err))

	_, err = service.Forward(context.Background(), &v1.ForwardRequest{
		Route: clusterroute.FromBinding(binding), Command: 3, Body: []byte{0xff},
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	_, err = service.Forward(context.Background(), &v1.ForwardRequest{
		Route: clusterroute.FromBinding(binding), Command: 1, NodeId: "another-node", NodeEpoch: "epoch-a",
	})
	require.Equal(t, codes.Aborted, status.Code(err))

	wrongService := binding
	wrongService.ServiceName = "chat"
	_, err = service.Forward(context.Background(), &v1.ForwardRequest{
		Route: clusterroute.FromBinding(wrongService), Command: 1,
	})
	require.Equal(t, codes.Aborted, status.Code(err))

	_, err = service.Forward(context.Background(), &v1.ForwardRequest{
		Route: clusterroute.FromBinding(binding), Command: 1, NodeEpoch: "orphan-epoch",
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
