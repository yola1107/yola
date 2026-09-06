package node

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
	require.NoError(t, locator.BindNode(context.Background(), "game", "player-a", "node-b"))
	_, err = server.forwardTo(context.Background(), stickyClaim{NodeID: "node-a", Epoch: epoch}, binding, 2, nil)
	require.Equal(t, codes.Aborted, status.Code(err))
}

func TestSessionUnbindDoesNotDeleteNewNode(t *testing.T) {
	locator := newMemoryLocator()
	server := newTestServer(t, Locator(locator))
	publishTestIdentity(server, nodeIdentity{serviceName: "game", nodeID: "node-a"})
	server.RegisterRawHandler(1, func(ctx context.Context, _ []byte) ([]byte, error) {
		sess, _ := FromContext(ctx)
		return nil, sess.UnbindNode(ctx)
	})
	require.NoError(t, locator.BindNode(context.Background(), "game", "player-a", "node-b"))

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
