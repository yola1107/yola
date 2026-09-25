package node

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"yola/locate"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestStopWaitsForSavedSessionBinding(t *testing.T) {
	for _, method := range []string{"BindNode", "UnbindNode"} {
		t.Run(method, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				store := newMemoryLocator()
				entered := make(chan context.Context, 1)
				release := make(chan struct{})
				releaseWrite := sync.OnceFunc(func() { close(release) })
				locator := &blockingSessionLocator{Locator: store, entered: entered, release: release}
				server, session := savedBindingSession(t, locator)
				t.Cleanup(func() { require.NoError(t, server.Stop(context.Background())) })
				t.Cleanup(releaseWrite)
				written := make(chan error, 1)
				go func() { written <- callSessionBinding(context.Background(), session, method) }()
				writeCtx := <-entered
				stopped := make(chan error, 1)
				go func() { stopped <- server.Stop(context.Background()) }()
				synctest.Wait()

				select {
				case err := <-stopped:
					t.Fatalf("Stop returned with an active %s: %v", method, err)
				default:
				}
				epoch, err := store.LocateNodeEpoch(context.Background(), "game", "node-a")
				require.NoError(t, err)
				require.Equal(t, "epoch-a", epoch)
				require.NoError(t, writeCtx.Err())
				require.Equal(t, codes.Unavailable, status.Code(callSessionBinding(context.Background(), session, method)))
				releaseWrite()
				require.NoError(t, <-written)
				require.NoError(t, <-stopped)
				require.NoError(t, server.Stop(context.Background()))
				_, err = store.LocateNodeEpoch(context.Background(), "game", "node-a")
				require.ErrorIs(t, err, locate.ErrNodeEpochNotFound)
			})
		})
	}
}

func TestSessionBindingDrainTimeoutKeepsEpoch(t *testing.T) {
	for _, method := range []string{"BindNode", "UnbindNode"} {
		t.Run(method, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				store := newMemoryLocator()
				entered := make(chan context.Context, 1)
				release := make(chan struct{})
				releaseWrite := sync.OnceFunc(func() { close(release) })
				locator := &blockingSessionLocator{Locator: store, entered: entered, release: release}
				server, session := savedBindingSession(t, locator)
				t.Cleanup(releaseWrite)
				written := make(chan error, 1)
				go func() { written <- callSessionBinding(context.Background(), session, method) }()
				writeCtx := <-entered
				stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()

				require.ErrorIs(t, server.Stop(stopCtx), context.DeadlineExceeded)
				synctest.Wait()
				require.ErrorIs(t, writeCtx.Err(), context.Canceled)
				require.Equal(t, codes.Unavailable, status.Code(callSessionBinding(context.Background(), session, method)))
				releaseWrite()
				require.NoError(t, <-written)
				require.ErrorIs(t, server.Stop(context.Background()), context.DeadlineExceeded)
				epoch, err := store.LocateNodeEpoch(context.Background(), "game", "node-a")
				require.NoError(t, err)
				require.Equal(t, "epoch-a", epoch)
			})
		})
	}
}

func TestBusinessDrainCanBindAndUnbindSavedSession(t *testing.T) {
	store := newMemoryLocator()
	var session Session
	server, session := savedBindingSession(t, store, Drain(func(ctx context.Context) error {
		if err := session.BindNode(ctx); err != nil {
			return err
		}
		return session.UnbindNode(ctx)
	}))

	require.NoError(t, server.Stop(context.Background()))
	_, err := store.LocateNode(context.Background(), "game", session.UID())
	require.ErrorIs(t, err, locate.ErrNodeNotFound)
	require.Equal(t, codes.Unavailable, status.Code(session.BindNode(context.Background())))
	require.Equal(t, codes.Unavailable, status.Code(session.UnbindNode(context.Background())))
}

func TestEpochLossCancelsSavedSessionBinding(t *testing.T) {
	for _, method := range []string{"BindNode", "UnbindNode"} {
		t.Run(method, func(t *testing.T) {
			store := newMemoryLocator()
			entered := make(chan context.Context, 1)
			locator := &blockingSessionLocator{Locator: store, entered: entered}
			server, session := savedBindingSession(t, locator)
			t.Cleanup(func() { require.NoError(t, server.Stop(context.Background())) })
			written := make(chan error, 1)
			go func() { written <- callSessionBinding(context.Background(), session, method) }()
			writeCtx := receiveNodeValue(t, entered)
			require.NoError(t, store.UnregisterNodeEpoch(context.Background(), "game", "node-a", "epoch-a"))
			require.NoError(t, store.RegisterNodeEpoch(context.Background(), "game", "node-a", "epoch-b", DefaultNodeEpochTTL))

			require.ErrorIs(t, server.lease.Load().renewOnce(context.Background()), locate.ErrNodeEpochConflict)
			require.ErrorIs(t, receiveNodeValue(t, written), context.Canceled)
			require.ErrorIs(t, context.Cause(writeCtx), locate.ErrNodeEpochConflict)
			require.Equal(t, codes.Unavailable, status.Code(callSessionBinding(context.Background(), session, method)))
			require.NoError(t, server.Stop(context.Background()))
			epoch, err := store.LocateNodeEpoch(context.Background(), "game", "node-a")
			require.NoError(t, err)
			require.Equal(t, "epoch-b", epoch)
		})
	}
}

func savedBindingSession(t *testing.T, locator locate.Locator, opts ...Option) (*Server, Session) {
	t.Helper()
	opts = append(opts, Locator(locator))
	server := newTestServer(t, opts...)
	require.NoError(t, locator.RegisterNodeEpoch(context.Background(), "game", "node-a", "epoch-a", DefaultNodeEpochTTL))
	publishTestIdentity(server, nodeIdentity{serviceName: "game", nodeID: "node-a", epoch: "epoch-a"})
	var session Session
	server.RegisterRawHandler(1, func(ctx context.Context, _ []byte) ([]byte, error) {
		session, _ = FromContext(ctx)
		return nil, nil
	})
	_, err := server.forward(context.Background(), testBinding("player-a", "conn-a"), 1, nil)
	require.NoError(t, err)
	require.NotNil(t, session)
	return server, session
}

func callSessionBinding(ctx context.Context, session Session, method string) error {
	if method == "BindNode" {
		return session.BindNode(ctx)
	}
	return session.UnbindNode(ctx)
}

type blockingSessionLocator struct {
	locate.Locator
	entered chan<- context.Context
	release <-chan struct{}
}

func (l *blockingSessionLocator) BindNode(ctx context.Context, service, uid, nodeID string) error {
	if err := l.wait(ctx); err != nil {
		return err
	}
	return l.Locator.BindNode(ctx, service, uid, nodeID)
}

func (l *blockingSessionLocator) UnbindNode(ctx context.Context, service, uid, nodeID string) error {
	if err := l.wait(ctx); err != nil {
		return err
	}
	return l.Locator.UnbindNode(ctx, service, uid, nodeID)
}

func (l *blockingSessionLocator) wait(ctx context.Context) error {
	l.entered <- ctx
	if l.release != nil {
		// 模拟已经发出、不会随取消撤销的存储写入。
		<-l.release
		return nil
	}
	<-ctx.Done()
	return ctx.Err()
}
