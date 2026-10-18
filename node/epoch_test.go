package node

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"yola/locate"

	"github.com/go-kratos/kratos/v3"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestEpochConflictPreventsRegistration(t *testing.T) {
	locator := newMemoryNodeStore()
	require.NoError(t, locator.RegisterNodeEpoch(
		context.Background(), "game", "node-a", "existing-epoch", DefaultNodeEpochTTL,
	))
	registrar := &countingRegistrar{}
	server := newTestServer(t, Stateful(locator), Registration(registrar))
	t.Cleanup(func() { require.NoError(t, server.Stop(context.Background())) })
	app := kratos.New(
		kratos.ID("node-a"),
		kratos.Name("game"),
		kratos.Server(server),
	)

	require.ErrorIs(t, app.Run(), locate.ErrNodeEpochConflict)
	require.Zero(t, registrar.registered.Load())
}

func TestEpochRenewFencesNodeWhenNodeIDTakenOver(t *testing.T) {
	locator := newMemoryNodeStore()
	server := newTestServer(t, Stateful(locator))
	identity := nodeIdentity{serviceName: "game", nodeID: "node-a", epoch: "epoch-a"}
	publishTestIdentity(server, identity)
	lease := server.lease.Load()
	t.Cleanup(func() { require.NoError(t, server.Stop(context.Background())) })
	require.NoError(t, locator.RegisterNodeEpoch(context.Background(), "game", "node-a", identity.epoch, DefaultNodeEpochTTL))
	require.NoError(t, lease.renewOnce(context.Background()))

	// 替换进程已取得同一 NodeID 的所有权。
	require.NoError(t, locator.UnregisterNodeEpoch(context.Background(), "game", "node-a", identity.epoch))
	require.NoError(t, locator.RegisterNodeEpoch(context.Background(), "game", "node-a", "epoch-b", DefaultNodeEpochTTL))

	require.ErrorIs(t, lease.renewOnce(context.Background()), locate.ErrNodeEpochConflict)
	_, err := server.forward(context.Background(), testBinding("player-a", "conn-a"), 1, nil)
	require.Equal(t, codes.Unavailable, status.Code(err))
	select {
	case <-server.fatalCtx.Done():
		require.ErrorIs(t, context.Cause(server.fatalCtx), locate.ErrNodeEpochConflict)
	default:
		t.Fatal("terminal epoch loss did not signal lifecycle failure")
	}
	server.failLifecycle(errors.New("later lifecycle failure"))
	require.ErrorIs(t, context.Cause(server.fatalCtx), locate.ErrNodeEpochConflict)
}

func TestEpochLossStopsApplicationAndDrainsNode(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	locator := newMemoryNodeStore()
	drained := make(chan struct{})
	registrar := &countingRegistrar{}
	server := newTestServer(t, Listener(lis), Stateful(locator), Registration(registrar), Drain(func(context.Context) error {
		close(drained)
		return nil
	}))
	app := kratos.New(
		kratos.ID("node-a"),
		kratos.Name("game"),
		kratos.Server(server),
		kratos.StopTimeout(time.Second),
	)

	done := make(chan error, 1)
	go func() { done <- app.Run() }()
	require.Eventually(t, func() bool {
		server.lifecycleMu.Lock()
		defer server.lifecycleMu.Unlock()
		return server.state == _stStarted && registrar.registered.Load() == 1
	}, time.Second, time.Millisecond)
	identity := server.currentIdentity()
	require.NoError(t, locator.UnregisterNodeEpoch(
		context.Background(), identity.serviceName, identity.nodeID, identity.epoch,
	))
	require.NoError(t, locator.RegisterNodeEpoch(
		context.Background(), identity.serviceName, identity.nodeID, "replacement", DefaultNodeEpochTTL,
	))
	require.ErrorIs(t, server.lease.Load().renewOnce(context.Background()), locate.ErrNodeEpochConflict)

	select {
	case runErr := <-done:
		require.ErrorIs(t, runErr, locate.ErrNodeEpochConflict)
	case <-time.After(time.Second):
		t.Fatal("application did not stop after terminal epoch loss")
	}
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("application stopped without draining Node state")
	}
}

func TestEpochRenewKeepsServingOnTransientFailure(t *testing.T) {
	locator := newMemoryNodeStore()
	identity := nodeIdentity{serviceName: "game", nodeID: "node-a", epoch: ""}
	lease := newEpochLease(locator, identity, time.Now().Add(DefaultNodeEpochTTL))
	defer func() { require.NoError(t, lease.stopRenewal(context.Background())) }()

	// 用非所有权错误模拟存储失败，原租约仍应有效。
	require.ErrorIs(t, lease.renewOnce(context.Background()), locate.ErrInvalidNodeEpoch)
	require.NoError(t, lease.valid())
}

func TestEpochLeaseReleaseClassifiesCleanupOutcome(t *testing.T) {
	transientErr := errors.New("redis unavailable")
	for _, test := range []struct {
		name        string
		err         error
		wantErr     error
		wantPending bool
	}{
		{name: "released"},
		{name: "already expired", err: locate.ErrNodeEpochNotFound},
		{name: "replaced", err: locate.ErrNodeEpochConflict},
		{name: "transient failure", err: transientErr, wantErr: transientErr, wantPending: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			locator := &epochUnregisterResultLocator{epochStore: newMemoryNodeStore(), err: test.err}
			identity := nodeIdentity{serviceName: "game", nodeID: "node-a", epoch: "epoch-a"}
			lease := newEpochLease(locator, identity, time.Now().Add(DefaultNodeEpochTTL))

			err := lease.release(context.Background())
			if test.wantErr == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, test.wantErr)
			}
			require.Equal(t, test.wantPending, !lease.released)
		})
	}
}

func TestEpochLeaseRenewalLifetime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &countingEpochStore{epochStore: newMemoryNodeStore()}
		lease, err := claimEpoch(context.Background(), store, nodeIdentity{serviceName: "game", nodeID: "node-a"})
		require.NoError(t, err)
		defer func() { require.NoError(t, lease.stopRenewal(context.Background())) }()
		var lost error
		require.NoError(t, <-lease.start(func(err error) { lost = err }))
		synctest.Wait()
		time.Sleep(_nodeEpochRenewInterval)
		synctest.Wait()
		require.EqualValues(t, 2, store.renewed.Load())
		require.NoError(t, lost)
		require.NoError(t, lease.stopRenewal(context.Background()))
		synctest.Wait()
		time.Sleep(2 * _nodeEpochRenewInterval)
		require.EqualValues(t, 2, store.renewed.Load())
		require.NoError(t, lease.release(context.Background()))
	})
}

func TestEpochLeaseExpiryCancelsAcceptedWork(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &controlledEpochLocator{Store: newMemoryNodeStore()}
		server := newTestServer(t, Stateful(store))
		identity := nodeIdentity{serviceName: "game", nodeID: "node-a", epoch: "epoch-a"}
		require.NoError(t, store.RegisterNodeEpoch(context.Background(), "game", "node-a", identity.epoch, DefaultNodeEpochTTL))
		publishTestIdentity(server, identity)
		t.Cleanup(func() { require.NoError(t, server.Stop(context.Background())) })
		lease := server.lease.Load()
		entered := make(chan struct{})
		server.RegisterRawHandler(1, func(ctx context.Context, _ []byte) ([]byte, error) {
			close(entered)
			<-ctx.Done()
			return nil, context.Cause(ctx)
		})
		require.NoError(t, <-lease.start(server.failLifecycle))
		store.setRenew(func(context.Context) error { return errors.New("store unavailable") })
		completed := make(chan error, 1)
		go func() {
			_, err := server.forward(context.Background(), testBinding("player-a", "conn-a"), 1, nil)
			completed <- err
		}()
		<-entered
		time.Sleep(DefaultNodeEpochTTL)
		synctest.Wait()

		require.ErrorIs(t, <-completed, errNodeEpochExpired)
		require.ErrorIs(t, context.Cause(server.fatalCtx), errNodeEpochExpired)
		require.True(t, server.requests.isClosed())
		require.True(t, server.deliveries.isClosed())
		require.ErrorIs(t, lease.renewOnce(context.Background()), errNodeEpochExpired)
	})
}

func TestEpochLeaseExpiresWhileRenewalIgnoresCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &controlledEpochLocator{Store: newMemoryNodeStore()}
		lease, err := claimEpoch(context.Background(), store, nodeIdentity{serviceName: "game", nodeID: "node-a"})
		require.NoError(t, err)
		release := make(chan struct{})
		releaseRenewal := sync.OnceFunc(func() { close(release) })
		t.Cleanup(func() {
			releaseRenewal()
			require.NoError(t, lease.stopRenewal(context.Background()))
		})
		lost := make(chan error, 1)
		require.NoError(t, <-lease.start(func(err error) { lost <- err }))
		blocked := make(chan context.Context, 1)
		store.setRenew(func(ctx context.Context) error {
			blocked <- ctx
			<-release
			return nil
		})
		time.Sleep(_nodeEpochRenewInterval)
		ctx := <-blocked
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		require.Equal(t, _nodeEpochRPCTimeout, time.Until(deadline))
		time.Sleep(DefaultNodeEpochTTL - _nodeEpochRenewInterval)
		synctest.Wait()
		require.ErrorIs(t, <-lost, errNodeEpochExpired)
		require.ErrorIs(t, lease.valid(), errNodeEpochExpired)

		stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.ErrorIs(t, lease.release(stopCtx), context.DeadlineExceeded)
		_, err = store.LocateNodeEpoch(context.Background(), "game", "node-a")
		require.NoError(t, err)
		releaseRenewal()
		require.NoError(t, lease.stopRenewal(context.Background()))
		require.ErrorIs(t, lease.valid(), errNodeEpochExpired)
		require.NoError(t, lease.release(context.Background()))
		_, err = store.LocateNodeEpoch(context.Background(), "game", "node-a")
		require.ErrorIs(t, err, locate.ErrNodeEpochNotFound)
	})
}

func TestEpochRenewalReturnsCurrentLossAfterEarlierCancellation(t *testing.T) {
	store := &controlledEpochLocator{Store: newMemoryNodeStore()}
	lease, err := claimEpoch(context.Background(), store, nodeIdentity{serviceName: "game", nodeID: "node-a"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, lease.release(context.Background())) })
	firstCause := errors.New("earlier epoch loss")
	store.setRenew(func(context.Context) error {
		lease.cancel(firstCause)
		return locate.ErrNodeEpochConflict
	})

	err = lease.renewOnce(lease.ctx)

	require.ErrorIs(t, err, locate.ErrNodeEpochConflict)
	require.EqualError(t, err, "node: epoch ownership lost: "+locate.ErrNodeEpochConflict.Error())
	require.NotSame(t, firstCause, err)
	require.Same(t, firstCause, context.Cause(lease.ctx))
}

func TestEpochLeaseUsesCallStartForDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &controlledEpochLocator{Store: newMemoryNodeStore()}
		lease, err := claimEpoch(context.Background(), store, nodeIdentity{serviceName: "game", nodeID: "node-a"})
		require.NoError(t, err)
		defer func() { require.NoError(t, lease.release(context.Background())) }()
		time.Sleep(_nodeEpochRenewInterval)
		store.setRenew(func(context.Context) error {
			time.Sleep(time.Second)
			return nil
		})
		require.NoError(t, lease.renewOnce(context.Background()))
		time.Sleep(DefaultNodeEpochTTL - time.Second)
		require.ErrorIs(t, lease.valid(), errNodeEpochExpired)
	})
}

func TestEpochLeaseRejectsLateSuccessfulRenewal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &controlledEpochLocator{Store: newMemoryNodeStore()}
		lease, err := claimEpoch(context.Background(), store, nodeIdentity{serviceName: "game", nodeID: "node-a"})
		require.NoError(t, err)
		defer func() { require.NoError(t, lease.release(context.Background())) }()
		time.Sleep(_nodeEpochRenewInterval)
		store.setRenew(func(context.Context) error {
			time.Sleep(_nodeEpochRPCTimeout + time.Second)
			return nil
		})
		require.ErrorIs(t, lease.renewOnce(context.Background()), context.DeadlineExceeded)
		time.Sleep(DefaultNodeEpochTTL - _nodeEpochRenewInterval - _nodeEpochRPCTimeout - time.Second)
		require.ErrorIs(t, lease.valid(), errNodeEpochExpired)
	})
}

func TestLeaseExpiryDuringNodeLookupRejectsHandler(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &delayedNodeLookup{Store: newMemoryNodeStore()}
		server := newTestServer(t, Stateful(store))
		t.Cleanup(func() { require.NoError(t, server.Stop(context.Background())) })
		publishTestIdentity(server, nodeIdentity{serviceName: "game", nodeID: "node-a", epoch: "epoch-a"})
		server.RegisterRawHandler(1, func(context.Context, []byte) ([]byte, error) {
			t.Error("expired lease dispatched a handler after Node lookup")
			return nil, nil
		})
		_, err := server.forwardTo(
			context.Background(),
			stickyClaim{NodeID: "node-a", Epoch: "epoch-a"},
			testBinding("player-a", "conn-a"),
			1,
			nil,
		)
		require.Equal(t, codes.Unavailable, status.Code(err))
	})
}

func TestExpiredPreparedLeaseRejectsUnboundRequestAndSavedSession(t *testing.T) {
	for _, test := range []struct {
		name string
		call func(*Server, locate.GateBinding) error
	}{
		{name: "unbound Forward", call: func(server *Server, binding locate.GateBinding) error {
			_, err := server.forward(context.Background(), binding, 1, nil)
			return err
		}},
		{name: "BindNode", call: func(server *Server, binding locate.GateBinding) error {
			return (requestSession{server: server, binding: binding}).BindNode(context.Background())
		}},
		{name: "UnbindNode", call: func(server *Server, binding locate.GateBinding) error {
			return (requestSession{server: server, binding: binding}).UnbindNode(context.Background())
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				store := newMemoryNodeStore()
				server := newTestServer(t, Stateful(store))
				t.Cleanup(func() { require.NoError(t, server.Stop(context.Background())) })
				publishTestIdentity(server, nodeIdentity{serviceName: "game", nodeID: "node-a", epoch: "epoch-a"})
				binding := testBinding("player-a", "conn-a")
				require.NoError(t, store.RegisterNodeEpoch(context.Background(), "game", "node-b", "epoch-b", DefaultNodeEpochTTL))
				require.NoError(t, store.BindNode(context.Background(), "game", binding.UID, "node-b", "epoch-b"))
				server.RegisterRawHandler(1, func(context.Context, []byte) ([]byte, error) {
					t.Error("expired lease dispatched a handler")
					return nil, nil
				})
				time.Sleep(DefaultNodeEpochTTL)

				require.Equal(t, codes.Unavailable, status.Code(test.call(server, binding)))
				nodeID, err := store.LocateNode(context.Background(), "game", binding.UID)
				require.NoError(t, err)
				require.Equal(t, "node-b", nodeID)
			})
		})
	}
}

type delayedNodeLookup struct{ Store }

func (*delayedNodeLookup) LocateNode(context.Context, string, string) (string, error) {
	time.Sleep(DefaultNodeEpochTTL)
	return "node-a", nil
}

type countingEpochStore struct {
	epochStore
	renewed atomic.Int32
}

func (s *countingEpochStore) RenewNodeEpoch(ctx context.Context, service, nodeID, epoch string, ttl time.Duration) error {
	s.renewed.Add(1)
	return s.epochStore.RenewNodeEpoch(ctx, service, nodeID, epoch, ttl)
}

type epochUnregisterResultLocator struct {
	epochStore
	err error
}

func (l *epochUnregisterResultLocator) UnregisterNodeEpoch(context.Context, string, string, string) error {
	return l.err
}
