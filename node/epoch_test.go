package node

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"yola/instance"
	"yola/locate"
	locateredis "yola/locate/redis"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-kratos/kratos/v3"
	"github.com/go-kratos/kratos/v3/registry"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestEpochConflictPreventsRegistration(t *testing.T) {
	locator := newMemoryLocator()
	require.NoError(t, locator.RegisterNodeEpoch(
		context.Background(), "game", "node-a", "existing-epoch", DefaultNodeEpochTTL,
	))
	server := newTestServer(t, Locator(locator))
	t.Cleanup(func() { require.NoError(t, server.Stop(context.Background())) })
	registrar := new(countingRegistrar)
	app := kratos.New(
		kratos.ID("node-a"),
		kratos.Name("game"),
		kratos.Metadata(instance.StickyMetadata()),
		kratos.BeforeStart(server.BeforeStart),
		kratos.Server(server),
		kratos.Registrar(registrar),
	)

	require.ErrorIs(t, app.Run(), locate.ErrNodeEpochConflict)
	require.Zero(t, registrar.registered.Load())
}

func TestStickyStartLifecycle(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := newTestServer(t, Listener(lis), Locator(newMemoryLocator()))
	t.Cleanup(func() { _ = server.Stop(context.Background()) })
	ctx := kratos.NewContext(context.Background(), nodeTestAppInfo{metadata: instance.StickyMetadata()})
	require.NoError(t, server.BeforeStart(ctx))

	done := make(chan error, 1)
	go func() { done <- server.Start(ctx) }()
	require.Eventually(t, func() bool {
		server.lifecycleMu.Lock()
		defer server.lifecycleMu.Unlock()
		return server.state == stStarted
	}, time.Second, time.Millisecond)
	require.EqualError(t, server.Start(ctx), "node: server is already started")
	require.NoError(t, server.Stop(context.Background()))
	require.ErrorIs(t, server.currentLease().ctx.Err(), context.Canceled)
	require.NoError(t, <-done)
}

func TestEpochRenewFencesNodeWhenNodeIDTakenOver(t *testing.T) {
	locator := newMemoryLocator()
	server := newTestServer(t, Locator(locator))
	identity := nodeIdentity{serviceName: "game", nodeID: "node-a", epoch: "epoch-a"}
	lease := newEpochLease(locator, identity)
	defer lease.stopRenewal()
	require.NoError(t, locator.RegisterNodeEpoch(context.Background(), "game", "node-a", identity.epoch, DefaultNodeEpochTTL))
	require.True(t, lease.renewOnce(context.Background(), server.failLifecycle))

	// A replacement process claimed the same NodeID.
	require.NoError(t, locator.UnregisterNodeEpoch(context.Background(), "game", "node-a", identity.epoch))
	require.NoError(t, locator.RegisterNodeEpoch(context.Background(), "game", "node-a", "epoch-b", DefaultNodeEpochTTL))

	require.False(t, lease.renewOnce(context.Background(), server.failLifecycle))
	_, err := server.forward(context.Background(), testBinding("player-a", "conn-a"), 1, nil)
	requireNodeDraining(t, err)
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
	locator := newMemoryLocator()
	drained := make(chan struct{})
	server := newTestServer(t, Listener(lis), Locator(locator), Drain(func(context.Context) error {
		close(drained)
		return nil
	}))
	registrar := new(countingRegistrar)
	app := kratos.New(
		kratos.ID("node-a"),
		kratos.Name("game"),
		kratos.Metadata(instance.StickyMetadata()),
		kratos.BeforeStart(server.BeforeStart),
		kratos.Server(server),
		kratos.Registrar(registrar),
		kratos.StopTimeout(time.Second),
	)

	done := make(chan error, 1)
	go func() { done <- app.Run() }()
	require.Eventually(t, func() bool {
		server.lifecycleMu.Lock()
		defer server.lifecycleMu.Unlock()
		return server.state == stStarted && registrar.registered.Load() == 1
	}, time.Second, time.Millisecond)
	identity := server.currentIdentity()
	require.NoError(t, locator.UnregisterNodeEpoch(
		context.Background(), identity.serviceName, identity.nodeID, identity.epoch,
	))
	require.NoError(t, locator.RegisterNodeEpoch(
		context.Background(), identity.serviceName, identity.nodeID, "replacement", DefaultNodeEpochTTL,
	))
	require.False(t, server.currentLease().renewOnce(context.Background(), server.failLifecycle))

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
	locator := newMemoryLocator()
	server := newTestServer(t, Locator(locator))
	identity := nodeIdentity{serviceName: "game", nodeID: "node-a", epoch: ""}
	lease := newEpochLease(locator, identity)
	defer lease.stopRenewal()

	// Invalid input stands in for a store error: it must not fence the process.
	require.True(t, lease.renewOnce(context.Background(), server.failLifecycle))
	require.False(t, server.requests.isClosed())
}

func TestReleasePendingEpochClassifiesCleanupOutcome(t *testing.T) {
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
			locator := &epochUnregisterResultLocator{Locator: newMemoryLocator(), err: test.err}
			server := newTestServer(t, Locator(locator))
			t.Cleanup(func() { _ = server.Stop(context.Background()) })
			publishTestIdentity(server, nodeIdentity{serviceName: "game", nodeID: "node-a", epoch: "epoch-a"})

			err := server.releaseEpoch(context.Background())
			if test.wantErr == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, test.wantErr)
			}
			require.Equal(t, test.wantPending, server.currentLease().releaseState != epochReleased)
			require.Equal(t, nodeIdentity{}, server.currentIdentity())
		})
	}
}

func TestReplacementStartWaitsForFailedDrainEpochExpiry(t *testing.T) {
	redisServer := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { require.NoError(t, redisClient.Close()) })
	appCtx := kratos.NewContext(context.Background(), nodeTestAppInfo{metadata: instance.StickyMetadata()})
	drainErr := errors.New("drain failed")

	current := newTestServer(t, Locator(locateredis.New(redisClient)), Drain(func(context.Context) error { return drainErr }))
	t.Cleanup(func() { _ = current.Stop(context.Background()) })
	blockedReplacement := newTestServer(t, Locator(locateredis.New(redisClient)))
	t.Cleanup(func() { _ = blockedReplacement.Stop(context.Background()) })
	require.NoError(t, current.BeforeStart(appCtx))
	require.ErrorIs(t, current.Stop(context.Background()), drainErr)
	require.ErrorIs(t, blockedReplacement.BeforeStart(appCtx), locate.ErrNodeEpochConflict)
	require.EqualError(t, blockedReplacement.BeforeStart(appCtx), "node: server is stopping or stopped")

	redisServer.FastForward(DefaultNodeEpochTTL + time.Millisecond)
	replacement := newTestServer(t, Locator(locateredis.New(redisClient)))
	t.Cleanup(func() { _ = replacement.Stop(context.Background()) })
	require.NoError(t, replacement.BeforeStart(appCtx))
}

type countingRegistrar struct {
	registered atomic.Int32
}

func TestEpochLeaseRenewalLifetime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &countingEpochStore{epochStore: newMemoryLocator()}
		lease, err := claimEpoch(context.Background(), store, nodeIdentity{serviceName: "game", nodeID: "node-a"})
		require.NoError(t, err)
		defer lease.stopRenewal()
		var lost error
		lease.start(func(err error) { lost = err })
		synctest.Wait()
		time.Sleep(nodeEpochRenewInterval)
		synctest.Wait()
		require.EqualValues(t, 1, store.renewed.Load())
		require.NoError(t, lost)
		lease.stopRenewal()
		synctest.Wait()
		time.Sleep(2 * nodeEpochRenewInterval)
		require.EqualValues(t, 1, store.renewed.Load())
		require.NoError(t, lease.release(context.Background()))
	})
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
	locate.Locator
	err error
}

func (l *epochUnregisterResultLocator) UnregisterNodeEpoch(context.Context, string, string, string) error {
	return l.err
}

func (r *countingRegistrar) Register(context.Context, *registry.ServiceInstance) error {
	r.registered.Add(1)
	return nil
}

func (*countingRegistrar) Deregister(context.Context, *registry.ServiceInstance) error { return nil }
