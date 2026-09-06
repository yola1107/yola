package node

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestForwardValidationAndErrors(t *testing.T) {
	handlerErr := status.Error(codes.Aborted, "rejected")
	server := newDispatchTestServer(t)
	t.Cleanup(func() { require.NoError(t, server.Stop(context.Background())) })
	server.RegisterRawHandler(1, func(context.Context, []byte) ([]byte, error) {
		return nil, handlerErr
	})

	invalid := testBinding("player-a", "conn-a")
	invalid.BindingToken = ""
	_, err := server.forward(context.Background(), invalid, 1, nil)
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	_, err = server.forward(context.Background(), testBinding("player-a", "conn-a"), 99, nil)
	require.Equal(t, codes.Unimplemented, status.Code(err))

	_, err = server.forward(context.Background(), testBinding("player-a", "conn-a"), 1, nil)
	require.ErrorIs(t, err, handlerErr)
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
	locator := newMemoryLocator()
	server := newTestServer(t, Locator(locator))
	const epoch = "epoch-a"
	publishTestIdentity(server, nodeIdentity{serviceName: "game", nodeID: "node-a", epoch: epoch})
	require.NoError(t, locator.RegisterNodeEpoch(context.Background(), "game", "node-a", epoch, DefaultNodeEpochTTL))
	require.NoError(t, locator.BindNode(context.Background(), "game", "player-a", "node-a"))
	server.RegisterRawHandler(1, func(context.Context, []byte) ([]byte, error) {
		return []byte("ok"), nil
	})
	binding := testBinding("player-a", "conn-a")

	_, err := server.forwardTo(context.Background(), stickyClaim{NodeID: "node-a", Epoch: "wrong-epoch"}, binding, 1, nil)
	require.Equal(t, codes.Aborted, status.Code(err))
	require.Contains(t, err.Error(), "node epoch mismatch")

	_, err = server.forwardTo(context.Background(), stickyClaim{NodeID: "node-a"}, binding, 1, nil)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, err.Error(), "invalid node claim")
}
