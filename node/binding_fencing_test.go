package node

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"yola/locate"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestBindingEpochLossCancelsAcceptedWork(t *testing.T) {
	for _, method := range []string{"BindNode", "UnbindNode"} {
		for _, loss := range []struct {
			name string
			err  error
		}{
			{name: "missing", err: locate.ErrNodeEpochNotFound},
			{name: "replaced", err: locate.ErrNodeEpochConflict},
		} {
			t.Run(method+"/"+loss.name, func(t *testing.T) {
				store := newMemoryLocator()
				server, session := savedBindingSession(t, store)
				t.Cleanup(func() { require.NoError(t, server.Stop(context.Background())) })
				entered := make(chan context.Context, 1)
				server.RegisterRawHandler(2, func(ctx context.Context, _ []byte) ([]byte, error) {
					entered <- ctx
					<-ctx.Done()
					return nil, ctx.Err()
				})
				finished := make(chan error, 1)
				go func() {
					_, err := server.forward(t.Context(), testBinding("other-player", "other-conn"), 2, nil)
					finished <- err
				}()
				workCtx := receiveNodeValue(t, entered)
				require.NoError(t, store.UnregisterNodeEpoch(t.Context(), "game", "node-a", "epoch-a"))
				if loss.name == "replaced" {
					require.NoError(t, store.RegisterNodeEpoch(t.Context(), "game", "node-a", "epoch-b", DefaultNodeEpochTTL))
				}

				err := callSessionBinding(t.Context(), session, method)
				require.Equal(t, codes.Unavailable, status.Code(err))
				require.ErrorIs(t, receiveNodeValue(t, finished), context.Canceled)
				require.ErrorIs(t, context.Cause(workCtx), loss.err)
				require.ErrorIs(t, context.Cause(server.fatalCtx), loss.err)
				require.ErrorIs(t, server.lease.Load().valid(), loss.err)
				require.True(t, server.requests.isClosed())
				require.True(t, server.deliveries.isClosed())
				_, err = server.forward(t.Context(), testBinding("player-a", "conn-a"), 1, nil)
				require.Equal(t, codes.Unavailable, status.Code(err))
				require.Equal(t, codes.Unavailable, status.Code(session.BindNode(t.Context())))
				require.Equal(t, codes.Unavailable, status.Code(session.UnbindNode(t.Context())))
				require.Equal(t, codes.Unavailable, status.Code(session.Push(t.Context(), 1, &emptypb.Empty{})))
				require.NoError(t, server.Stop(t.Context()))
				if loss.name == "replaced" {
					epoch, locateErr := store.LocateNodeEpoch(t.Context(), "game", "node-a")
					require.NoError(t, locateErr)
					require.Equal(t, "epoch-b", epoch)
				}
			})
		}
	}
}

func TestBindingEpochLossPreservesRecordedCause(t *testing.T) {
	server, _ := savedBindingSession(t, newMemoryLocator())
	t.Cleanup(func() { require.NoError(t, server.Stop(context.Background())) })
	lease := server.lease.Load()
	firstCause := errors.New("earlier epoch loss")
	lease.cancel(firstCause)

	err := server.handleBindingError(locate.ErrNodeEpochConflict)

	require.Equal(t, codes.Unavailable, status.Code(err))
	require.Same(t, firstCause, context.Cause(lease.ctx))
	require.Same(t, firstCause, context.Cause(server.fatalCtx))
	require.True(t, server.requests.isClosed())
	require.True(t, server.deliveries.isClosed())
}

func TestBindingOtherErrorsDoNotRevokeEpoch(t *testing.T) {
	for _, method := range []string{"BindNode", "UnbindNode"} {
		for _, failure := range []struct {
			name string
			err  error
			code codes.Code
		}{
			{name: "dependency", err: errors.New("temporary dependency failure"), code: codes.Unavailable},
			{name: "canceled", err: context.Canceled, code: codes.Canceled},
			{name: "deadline", err: context.DeadlineExceeded, code: codes.DeadlineExceeded},
			{name: "invalid", err: locate.ErrInvalidNodeEpoch, code: codes.Internal},
		} {
			t.Run(method+"/"+failure.name, func(t *testing.T) {
				store := &bindingErrorLocator{Locator: newMemoryLocator(), err: failure.err}
				server, session := savedBindingSession(t, store)
				t.Cleanup(func() { require.NoError(t, server.Stop(context.Background())) })
				err := callSessionBinding(t.Context(), session, method)
				if errors.Is(failure.err, context.Canceled) || errors.Is(failure.err, context.DeadlineExceeded) {
					require.ErrorIs(t, err, failure.err)
				} else {
					require.Equal(t, failure.code, status.Code(err))
				}
				require.NoError(t, server.lease.Load().valid())
				require.NoError(t, context.Cause(server.fatalCtx))
				require.False(t, server.requests.isClosed())
				require.False(t, server.deliveries.isClosed())
				_, err = server.forward(t.Context(), testBinding("player-a", "conn-a"), 1, nil)
				require.NoError(t, err)
			})
		}
	}
}

func TestBindingLossCannotBeUndoneByLateRenewal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &delayedBindingRenewal{Locator: newMemoryLocator(), entered: make(chan struct{}), release: make(chan struct{})}
		unblock := sync.OnceFunc(func() { close(store.release) })
		server, session := savedBindingSession(t, store)
		t.Cleanup(func() { require.NoError(t, server.Stop(context.Background())) })
		t.Cleanup(unblock)
		lease := server.lease.Load()
		require.NoError(t, <-lease.start(server.failLifecycle))
		time.Sleep(nodeEpochRenewInterval)
		<-store.entered
		deadline := *lease.deadline.Load()
		require.NoError(t, store.UnregisterNodeEpoch(t.Context(), "game", "node-a", "epoch-a"))
		require.NoError(t, store.RegisterNodeEpoch(t.Context(), "game", "node-a", "new", DefaultNodeEpochTTL))
		require.Equal(t, codes.Unavailable, status.Code(session.BindNode(t.Context())))
		cause := context.Cause(lease.ctx)
		require.ErrorIs(t, cause, locate.ErrNodeEpochConflict)
		synctest.Wait()
		unblock()
		<-lease.done
		require.Same(t, cause, context.Cause(lease.ctx))
		require.Same(t, cause, context.Cause(server.fatalCtx))
		require.ErrorIs(t, lease.valid(), locate.ErrNodeEpochConflict)
		require.Equal(t, deadline, *lease.deadline.Load())
		require.True(t, server.requests.isClosed())
		require.True(t, server.deliveries.isClosed())
	})
}

func TestBindingEpochLossDuringDrainPreservesNewEpoch(t *testing.T) {
	for _, method := range []string{"BindNode", "UnbindNode"} {
		t.Run(method, func(t *testing.T) {
			store := newMemoryLocator()
			var session Session
			var drained int
			server, session := savedBindingSession(t, store, Drain(func(ctx context.Context) error {
				drained++
				return callSessionBinding(ctx, session, method)
			}))
			require.NoError(t, store.UnregisterNodeEpoch(t.Context(), "game", "node-a", "epoch-a"))
			require.NoError(t, store.RegisterNodeEpoch(t.Context(), "game", "node-a", "new", DefaultNodeEpochTTL))
			require.NoError(t, store.BindNode(t.Context(), "game", "player-a", "node-a", "new"))
			require.Equal(t, codes.Unavailable, status.Code(server.Stop(t.Context())))
			require.Equal(t, codes.Unavailable, status.Code(server.Stop(t.Context())))
			require.Equal(t, 1, drained)
			epoch, err := store.LocateNodeEpoch(t.Context(), "game", "node-a")
			require.NoError(t, err)
			require.Equal(t, "new", epoch)
			nodeID, err := store.LocateNode(t.Context(), "game", "player-a")
			require.NoError(t, err)
			require.Equal(t, "node-a", nodeID)
		})
	}
}

type delayedBindingRenewal struct {
	locate.Locator
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (l *delayedBindingRenewal) RenewNodeEpoch(ctx context.Context, service, nodeID, epoch string, ttl time.Duration) error {
	if err := l.Locator.RenewNodeEpoch(ctx, service, nodeID, epoch, ttl); err != nil {
		return err
	}
	if l.calls.Add(1) == 2 {
		close(l.entered)
		// 续租已执行成功，只延迟响应，让另一写入先发现存储失权。
		<-l.release
	}
	return nil
}

type bindingErrorLocator struct {
	locate.Locator
	err error
}

func (l *bindingErrorLocator) BindNode(context.Context, string, string, string, string) error {
	return l.err
}

func (l *bindingErrorLocator) UnbindNode(context.Context, string, string, string, string) error {
	return l.err
}
