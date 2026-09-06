package node

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	v1 "yola/api/cluster/v1"
	"yola/instance"
	"yola/internal/clusterroute"
	"yola/locate"

	"github.com/go-kratos/kratos/v3"
	"github.com/stretchr/testify/require"
	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestBeforeStartValidatesApplicationIdentity(t *testing.T) {
	tests := []struct {
		name string
		app  invalidNodeAppInfo
	}{
		{name: "service name", app: invalidNodeAppInfo{id: "node-a"}},
		{name: "instance ID", app: invalidNodeAppInfo{name: "game"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newTestServer(t)
			t.Cleanup(func() { require.NoError(t, server.Stop(context.Background())) })
			ctx := kratos.NewContext(context.Background(), test.app)

			require.EqualError(t, server.BeforeStart(ctx), "node: invalid application identity")
		})
	}
}

func TestBeforeStartFailureReleasesInternalGRPCListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	server := newTestServer(t, Listener(listener))
	_, err = server.Endpoint()
	require.NoError(t, err)

	ctx := kratos.NewContext(context.Background(), invalidNodeAppInfo{name: "game"})
	require.EqualError(t, server.BeforeStart(ctx), "node: invalid application identity")
	rebound, err := net.Listen("tcp", address)
	require.NoError(t, err)
	require.NoError(t, rebound.Close())
	require.NoError(t, server.Stop(context.Background()))
}

func TestBeforeStartRejectsStoppedServerBeforeEpochRegistration(t *testing.T) {
	locator := newMemoryLocator()
	server := newTestServer(t, Locator(locator))
	require.NoError(t, server.Stop(context.Background()))
	ctx := kratos.NewContext(context.Background(), nodeTestAppInfo{metadata: instance.StickyMetadata()})

	require.EqualError(t, server.BeforeStart(ctx), "node: server is stopping or stopped")
	_, err := locator.LocateNodeEpoch(context.Background(), "game", "node-a")
	require.ErrorIs(t, err, locate.ErrNodeEpochNotFound)
}

func TestBeforeStartReentryWhilePreparingKeepsOwnerState(t *testing.T) {
	base := newMemoryLocator()
	pinged := make(chan context.Context, 1)
	server := newTestServer(t, Locator(&initializationNodeLocator{Locator: base, pinged: pinged}))
	t.Cleanup(func() { require.NoError(t, server.Stop(context.Background())) })
	baseCtx, cancel := context.WithCancel(context.Background())
	ctx := kratos.NewContext(baseCtx, nodeTestAppInfo{metadata: instance.StickyMetadata()})
	prepared := make(chan error, 1)
	go func() { prepared <- server.BeforeStart(ctx) }()
	select {
	case <-pinged:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Node locator ping")
	}

	require.EqualError(t, server.BeforeStart(ctx), "node: server is already preparing or prepared")
	server.lifecycleMu.Lock()
	require.Equal(t, stPreparing, server.state)
	server.lifecycleMu.Unlock()
	require.False(t, server.requests.isClosed())
	cancel()
	require.ErrorIs(t, receiveNodeValue(t, prepared), context.Canceled)
}

func TestBeforeStartReentryMakesPreparedServerTerminal(t *testing.T) {
	locator := newMemoryLocator()
	server := newTestServer(t, Locator(locator))
	t.Cleanup(func() { require.NoError(t, server.Stop(context.Background())) })
	ctx := kratos.NewContext(context.Background(), nodeTestAppInfo{metadata: instance.StickyMetadata()})

	require.NoError(t, server.BeforeStart(ctx))
	identity := server.currentIdentity()
	require.NotEmpty(t, identity.epoch)
	require.EqualError(t, server.BeforeStart(ctx), "node: server is already preparing or prepared")
	require.True(t, server.requests.isClosed())
	require.EqualError(t, server.Start(ctx), "node: server is stopping or stopped")
	epoch, err := locator.LocateNodeEpoch(context.Background(), identity.serviceName, identity.nodeID)
	require.NoError(t, err)
	require.Equal(t, identity.epoch, epoch)

	require.NoError(t, server.Stop(context.Background()))
	_, err = locator.LocateNodeEpoch(context.Background(), identity.serviceName, identity.nodeID)
	require.ErrorIs(t, err, locate.ErrNodeEpochNotFound)
}

func TestStopWaitsForBeforeStartEpochRollback(t *testing.T) {
	for _, test := range []struct {
		name        string
		blockBefore bool
	}{
		{name: "before registration", blockBefore: true},
		{name: "after registration"},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := newMemoryLocator()
			registrationReached := make(chan struct{})
			releaseRegistration := make(chan struct{})
			unregisterStarted := make(chan struct{})
			releaseUnregister := make(chan struct{})
			releaseRegister := sync.OnceFunc(func() { close(releaseRegistration) })
			releaseCleanup := sync.OnceFunc(func() { close(releaseUnregister) })
			locator := &blockingEpochRegistrationLocator{
				Locator:             base,
				blockBefore:         test.blockBefore,
				registrationReached: registrationReached,
				releaseRegistration: releaseRegistration,
				unregisterStarted:   unregisterStarted,
				releaseUnregister:   releaseUnregister,
			}
			server := newTestServer(t, Locator(locator))
			t.Cleanup(func() {
				releaseRegister()
				releaseCleanup()
				_ = server.grpcServer.Stop(context.Background())
				_ = server.gateways.Close()
			})
			ctx := kratos.NewContext(context.Background(), nodeTestAppInfo{metadata: instance.StickyMetadata()})
			prepared := make(chan error, 1)
			go func() { prepared <- server.BeforeStart(ctx) }()
			waitNodeSignal(t, registrationReached)
			require.Equal(t, nodeIdentity{}, server.currentIdentity())

			if test.blockBefore {
				_, err := base.LocateNodeEpoch(context.Background(), "game", "node-a")
				require.ErrorIs(t, err, locate.ErrNodeEpochNotFound)
			} else {
				epoch, err := base.LocateNodeEpoch(context.Background(), "game", "node-a")
				require.NoError(t, err)
				require.NotEmpty(t, epoch)
			}

			stopped := make(chan error, 1)
			go func() { stopped <- server.Stop(context.Background()) }()
			require.Eventually(t, server.requests.isClosed, time.Second, time.Millisecond)
			select {
			case err := <-stopped:
				t.Fatalf("Stop returned before epoch registration completed: %v", err)
			default:
			}

			releaseRegister()
			waitNodeSignal(t, unregisterStarted)
			select {
			case err := <-stopped:
				t.Fatalf("Stop returned before epoch rollback completed: %v", err)
			default:
			}
			releaseCleanup()
			require.EqualError(t, receiveNodeValue(t, prepared), "node: server is stopping or stopped")
			require.NoError(t, receiveNodeValue(t, stopped))
			require.Equal(t, nodeIdentity{}, server.currentIdentity())
			require.True(t, server.requests.isClosed())
			_, err := base.LocateNodeEpoch(context.Background(), "game", "node-a")
			require.ErrorIs(t, err, locate.ErrNodeEpochNotFound)

			replacement := newTestServer(t, Locator(base))
			require.NoError(t, replacement.BeforeStart(ctx))
			require.NoError(t, replacement.Stop(context.Background()))
		})
	}
}

func TestStopTimeoutLeavesBeforeStartOwnerToReleaseEpoch(t *testing.T) {
	base := newMemoryLocator()
	registrationReached := make(chan struct{})
	releaseRegistration := make(chan struct{})
	unregistered := make(chan struct{})
	releaseRegister := sync.OnceFunc(func() { close(releaseRegistration) })
	locator := &blockingEpochRegistrationLocator{
		Locator:             base,
		registrationReached: registrationReached,
		releaseRegistration: releaseRegistration,
		unregistered:        unregistered,
	}
	server := newTestServer(t, Locator(locator))
	t.Cleanup(func() {
		releaseRegister()
		_ = server.grpcServer.Stop(context.Background())
		_ = server.gateways.Close()
	})
	ctx := kratos.NewContext(context.Background(), nodeTestAppInfo{metadata: instance.StickyMetadata()})
	prepared := make(chan error, 1)
	go func() { prepared <- server.BeforeStart(ctx) }()
	waitNodeSignal(t, registrationReached)

	stopCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, server.Stop(stopCtx), context.DeadlineExceeded)
	if _, err := base.LocateNodeEpoch(context.Background(), "game", "node-a"); err != nil {
		t.Fatalf("registered epoch disappeared before preparation owner resumed: %v", err)
	}
	releaseRegister()
	require.EqualError(t, receiveNodeValue(t, prepared), "node: server is stopping or stopped")
	waitNodeSignal(t, unregistered)
	_, err := base.LocateNodeEpoch(context.Background(), "game", "node-a")
	require.ErrorIs(t, err, locate.ErrNodeEpochNotFound)

	replacement := newTestServer(t, Locator(base))
	require.NoError(t, replacement.BeforeStart(ctx))
	require.NoError(t, replacement.Stop(context.Background()))
}

func TestRollbackPreparationPreservesCauseAndEpochForRetry(t *testing.T) {
	prepareErr := errors.New("prepare failed")
	cleanupErr := errors.New("cleanup failed")
	locator := &failingEpochUnregisterLocator{Locator: newMemoryLocator(), err: cleanupErr}
	server := newTestServer(t, Locator(locator))
	t.Cleanup(func() { require.NoError(t, server.Stop(context.Background())) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	lease := newEpochLease(locator, nodeIdentity{serviceName: "game", nodeID: "node-a", epoch: "epoch-a"})
	err := server.rollbackPreparation(ctx, lease, prepareErr)
	require.ErrorIs(t, err, prepareErr)
	require.ErrorIs(t, err, cleanupErr)
	require.ErrorContains(t, err, "node: roll back epoch")
	require.NotNil(t, locator.ctx)
	_, hasDeadline := locator.ctx.Deadline()
	require.True(t, hasDeadline)
	require.Same(t, lease, server.currentLease())
	require.NoError(t, lease.retryRelease(context.Background()))
	require.Equal(t, epochReleased, lease.releaseState)
	require.True(t, server.requests.isClosed())
}

func TestRollbackPreparationBoundsUncanceledEpochCleanup(t *testing.T) {
	locator := &blockingEpochUnregisterLocator{Locator: newMemoryLocator()}
	server := newTestServer(t, Locator(locator), PushTimeout(20*time.Millisecond))
	t.Cleanup(func() { require.NoError(t, server.Stop(context.Background())) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	started := time.Now()
	lease := newEpochLease(locator, nodeIdentity{
		serviceName: "game", nodeID: "node-a", epoch: "epoch-a",
	})
	err := server.rollbackPreparation(ctx, lease, errors.New("prepare failed"))
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(started), 200*time.Millisecond)
	_, hasDeadline := locator.ctx.Deadline()
	require.True(t, hasDeadline)
}

func TestHandlerRegistrationFrozenDuringPreparation(t *testing.T) {
	pinged := make(chan context.Context, 1)
	locator := &initializationNodeLocator{Locator: newMemoryLocator(), pinged: pinged}
	server := newTestServer(t, Locator(locator))
	t.Cleanup(func() { require.NoError(t, server.Stop(context.Background())) })
	ctx, cancel := context.WithCancel(kratos.NewContext(
		context.Background(),
		nodeTestAppInfo{metadata: instance.StickyMetadata()},
	))
	defer cancel()
	initialized := make(chan error, 1)
	go func() { initialized <- server.BeforeStart(ctx) }()

	var initializationCtx context.Context
	select {
	case initializationCtx = <-pinged:
	case <-time.After(time.Second):
		t.Fatal("Node initialization did not reach Locator.Ping")
	}
	assertHandlerRegistrationFrozen(t, server)
	cancel()
	require.ErrorIs(t, initializationCtx.Err(), context.Canceled)
	require.ErrorIs(t, <-initialized, context.Canceled)
}

func TestServerTransportTimeoutRecoveryAndRepeatedStop(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := newTestServer(t, Listener(lis), HandlerTimeout(20*time.Millisecond))
	server.RegisterRawHandler(1, func(ctx context.Context, _ []byte) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	server.RegisterRawHandler(2, func(context.Context, []byte) ([]byte, error) {
		panic("failed")
	})
	server.RegisterRawHandler(3, func(ctx context.Context, _ []byte) ([]byte, error) {
		sess, _ := FromContext(ctx)
		return nil, sess.Push(ctx, 1, wrapperspb.String("push"))
	})
	done := make(chan error, 1)
	ctx := kratos.NewContext(context.Background(), nodeTestAppInfo{})
	require.NoError(t, server.BeforeStart(ctx))
	go func() { done <- server.Start(ctx) }()

	conn, err := grpcgo.NewClient(lis.Addr().String(), grpcgo.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	client := v1.NewNodeClient(conn)
	req := &v1.ForwardRequest{Route: clusterroute.FromBinding(testBinding("player-a", "conn-a"))}

	req.Command = 1
	_, err = client.Forward(context.Background(), req)
	require.Equal(t, codes.DeadlineExceeded, status.Code(err))

	req.Command = 2
	_, err = client.Forward(context.Background(), req)
	require.Equal(t, codes.Internal, status.Code(err))

	require.NoError(t, server.Stop(context.Background()))
	require.NoError(t, server.Stop(context.Background()))
	require.NoError(t, <-done)

	_, err = server.forward(context.Background(), testBinding("player-a", "conn-a"), 3, nil)
	requireNodeDraining(t, err)
}

func TestServerStartFailureClosesResources(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	require.NoError(t, lis.Close())
	server := newTestServer(t, Listener(lis))
	server.RegisterRawHandler(1, func(ctx context.Context, _ []byte) ([]byte, error) {
		sess, _ := FromContext(ctx)
		return nil, sess.Push(ctx, 1, wrapperspb.String("push"))
	})
	app := kratos.New(
		kratos.ID("node-a"),
		kratos.Name("game"),
		kratos.BeforeStart(server.BeforeStart),
		kratos.Server(server),
	)

	require.Error(t, app.Run())
	_, err = server.forward(context.Background(), testBinding("player-a", "conn-a"), 1, nil)
	requireNodeDraining(t, err)
}

func TestStickyStartFailureKeepsEpochCleanupForStop(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	require.NoError(t, lis.Close())
	base := newMemoryLocator()
	cleanupErr := errors.New("cleanup failed")
	locator := &failingEpochUnregisterLocator{Locator: base, err: cleanupErr}
	server := newTestServer(t, Listener(lis), Locator(locator))
	ctx := kratos.NewContext(context.Background(), nodeTestAppInfo{metadata: instance.StickyMetadata()})
	require.NoError(t, server.BeforeStart(ctx))
	require.NoError(t, server.currentLease().ctx.Err())
	require.ErrorIs(t, server.Start(ctx), cleanupErr)
	require.ErrorIs(t, server.currentLease().ctx.Err(), context.Canceled)
	require.Empty(t, server.currentIdentity().epoch)
	require.Equal(t, epochReleaseFailed, server.currentLease().releaseState)
	require.True(t, server.requests.isClosed())
	require.EqualError(t, server.BeforeStart(ctx), "node: server is stopping or stopped")
	require.NoError(t, server.Stop(context.Background()))
	require.Equal(t, epochReleased, server.currentLease().releaseState)
	// Epoch must be free for a replacement process with the same NodeID.
	require.NoError(t, base.RegisterNodeEpoch(context.Background(), "game", "node-a", "fresh", DefaultNodeEpochTTL))
}

func TestStopDrainsOnceAndRejectsForward(t *testing.T) {
	var drains atomic.Int32
	server := newTestServer(t, Drain(func(context.Context) error {
		drains.Add(1)
		return nil
	}))
	server.RegisterRawHandler(1, func(context.Context, []byte) ([]byte, error) {
		return []byte("ok"), nil
	})
	binding := locate.GateBinding{
		ServiceName: "game", UID: "u1", GateID: "g1", GateEndpoint: "grpc://127.0.0.1:1",
		ConnID: "c1", BindingToken: "t1",
	}
	require.NoError(t, server.Stop(context.Background()))
	require.NoError(t, server.Stop(context.Background()))
	require.Equal(t, int32(1), drains.Load())
	_, err := server.forward(context.Background(), binding, 1, []byte("x"))
	requireNodeDraining(t, err)
}

func TestStopWaitsForDrainBeforeReleasingEpoch(t *testing.T) {
	locator := newMemoryLocator()
	const epoch = "epoch-a"
	require.NoError(t, locator.RegisterNodeEpoch(context.Background(), "game", "node-a", epoch, DefaultNodeEpochTTL))
	drainStarted := make(chan struct{})
	releaseDrain := make(chan struct{})
	server := newTestServer(t, Locator(locator), Drain(func(context.Context) error {
		close(drainStarted)
		<-releaseDrain
		return nil
	}))
	publishTestIdentity(server, nodeIdentity{serviceName: "game", nodeID: "node-a", epoch: epoch})

	stopDone := make(chan error, 1)
	go func() { stopDone <- server.Stop(context.Background()) }()
	<-drainStarted
	located, err := locator.LocateNodeEpoch(context.Background(), "game", "node-a")
	require.NoError(t, err)
	require.Equal(t, epoch, located)

	close(releaseDrain)
	require.NoError(t, <-stopDone)
	_, err = locator.LocateNodeEpoch(context.Background(), "game", "node-a")
	require.ErrorIs(t, err, locate.ErrNodeEpochNotFound)
}

func TestStopRetriesTransientEpochCleanup(t *testing.T) {
	base := newMemoryLocator()
	const epoch = "epoch-a"
	require.NoError(t, base.RegisterNodeEpoch(context.Background(), "game", "node-a", epoch, DefaultNodeEpochTTL))
	cleanupErr := errors.New("cleanup failed")
	locator := &failingEpochUnregisterLocator{Locator: base, err: cleanupErr}
	server := newTestServer(t, Locator(locator))
	publishTestIdentity(server, nodeIdentity{serviceName: "game", nodeID: "node-a", epoch: epoch})

	require.ErrorIs(t, server.Stop(context.Background()), cleanupErr)
	require.Equal(t, int32(1), locator.calls.Load())
	located, err := base.LocateNodeEpoch(context.Background(), "game", "node-a")
	require.NoError(t, err)
	require.Equal(t, epoch, located)

	const retryCount = 16
	retry := make(chan struct{})
	retryErrors := make(chan error, retryCount)
	for range retryCount {
		go func() {
			<-retry
			retryErrors <- server.Stop(context.Background())
		}()
	}
	close(retry)
	for range retryCount {
		require.NoError(t, <-retryErrors)
	}
	require.Equal(t, int32(2), locator.calls.Load())
	_, err = base.LocateNodeEpoch(context.Background(), "game", "node-a")
	require.ErrorIs(t, err, locate.ErrNodeEpochNotFound)
}

func TestStopDrainFailureKeepsEpochUntilTTL(t *testing.T) {
	locator := newMemoryLocator()
	const epoch = "epoch-a"
	require.NoError(t, locator.RegisterNodeEpoch(context.Background(), "game", "node-a", epoch, DefaultNodeEpochTTL))
	drainErr := errors.New("drain failed")
	server := newTestServer(t, Locator(locator), Drain(func(context.Context) error { return drainErr }))
	publishTestIdentity(server, nodeIdentity{serviceName: "game", nodeID: "node-a", epoch: epoch})
	renewCtx := server.currentLease().ctx

	require.ErrorIs(t, server.Stop(context.Background()), drainErr)
	located, err := locator.LocateNodeEpoch(context.Background(), "game", "node-a")
	require.NoError(t, err)
	require.Equal(t, epoch, located)
	require.ErrorIs(
		t, locator.RegisterNodeEpoch(context.Background(), "game", "node-a", "epoch-b", DefaultNodeEpochTTL),
		locate.ErrNodeEpochConflict,
	)
	require.ErrorIs(t, renewCtx.Err(), context.Canceled)
	require.ErrorIs(t, server.Stop(context.Background()), drainErr)
	located, err = locator.LocateNodeEpoch(context.Background(), "game", "node-a")
	require.NoError(t, err)
	require.Equal(t, epoch, located)
}

func TestStopWaitsForAcceptedForwardBeforeReleasingEpoch(t *testing.T) {
	locator := newMemoryLocator()
	const epoch = "epoch-a"
	require.NoError(t, locator.RegisterNodeEpoch(context.Background(), "game", "node-a", epoch, DefaultNodeEpochTTL))
	require.NoError(t, locator.BindNode(context.Background(), "game", "player-a", "node-a"))
	server := newTestServer(t, Locator(locator))
	publishTestIdentity(server, nodeIdentity{serviceName: "game", nodeID: "node-a", epoch: epoch})
	entered := make(chan struct{})
	release := make(chan struct{})
	server.RegisterRawHandler(1, func(ctx context.Context, _ []byte) ([]byte, error) {
		close(entered)
		<-release
		sess, _ := FromContext(ctx)
		return nil, sess.BindNode(ctx)
	})

	forwardDone := make(chan error, 1)
	go func() {
		_, err := server.forwardTo(
			context.Background(), stickyClaim{NodeID: "node-a", Epoch: epoch},
			testBinding("player-a", "conn-a"), 1, nil,
		)
		forwardDone <- err
	}()
	<-entered
	stopDone := make(chan error, 1)
	go func() { stopDone <- server.Stop(context.Background()) }()
	require.Eventually(t, server.requests.isClosed, time.Second, time.Millisecond)

	located, err := locator.LocateNodeEpoch(context.Background(), "game", "node-a")
	require.NoError(t, err)
	require.Equal(t, epoch, located)
	select {
	case stopErr := <-stopDone:
		t.Fatalf("Stop returned before Forward completed: %v", stopErr)
	case <-time.After(20 * time.Millisecond):
	}

	close(release)
	require.NoError(t, <-forwardDone)
	require.NoError(t, <-stopDone)
	_, err = locator.LocateNodeEpoch(context.Background(), "game", "node-a")
	require.ErrorIs(t, err, locate.ErrNodeEpochNotFound)
}

func TestStopTimeoutDoesNotReleaseEpoch(t *testing.T) {
	locator := newMemoryLocator()
	const epoch = "epoch-a"
	require.NoError(t, locator.RegisterNodeEpoch(context.Background(), "game", "node-a", epoch, DefaultNodeEpochTTL))
	server := newTestServer(t, Locator(locator))
	publishTestIdentity(server, nodeIdentity{serviceName: "game", nodeID: "node-a", epoch: epoch})
	entered := make(chan struct{})
	release := make(chan struct{})
	server.RegisterRawHandler(1, func(context.Context, []byte) ([]byte, error) {
		close(entered)
		<-release
		return nil, nil
	})

	forwardDone := make(chan error, 1)
	go func() {
		_, err := server.forward(context.Background(), testBinding("player-a", "conn-a"), 1, nil)
		forwardDone <- err
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, server.Stop(ctx), context.DeadlineExceeded)

	located, err := locator.LocateNodeEpoch(context.Background(), "game", "node-a")
	require.NoError(t, err)
	require.Equal(t, epoch, located)
	close(release)
	require.NoError(t, <-forwardDone)
}

func TestBeforeStartValidatesStickyMetadata(t *testing.T) {
	tests := []struct {
		name     string
		metadata map[string]string
		locator  bool
		wantErr  string
	}{
		{name: "sticky", metadata: instance.StickyMetadata(), locator: true},
		{name: "stateless"},
		{
			name: "invalid", metadata: map[string]string{instance.StickyMetadataKey: "invalid"},
			wantErr: "invalid sticky metadata",
		},
		{
			name: "mismatch", locator: true,
			wantErr: "sticky metadata and Node locator mismatch",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var opts []Option
			if test.locator {
				opts = append(opts, Locator(newMemoryLocator()))
			}
			server := newTestServer(t, opts...)
			t.Cleanup(func() { require.NoError(t, server.Stop(context.Background())) })
			ctx := kratos.NewContext(context.Background(), nodeTestAppInfo{metadata: test.metadata})

			err := server.BeforeStart(ctx)
			if test.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, test.wantErr)
		})
	}
}

type initializationNodeLocator struct {
	locate.Locator
	pinged chan<- context.Context
}

type blockingEpochRegistrationLocator struct {
	locate.Locator
	blockBefore         bool
	registrationReached chan struct{}
	releaseRegistration <-chan struct{}
	unregisterStarted   chan struct{}
	releaseUnregister   <-chan struct{}
	unregistered        chan struct{}
}

type failingEpochUnregisterLocator struct {
	locate.Locator
	ctx   context.Context
	err   error
	calls atomic.Int32
}

type blockingEpochUnregisterLocator struct {
	locate.Locator
	ctx   context.Context
	calls atomic.Int32
}

func (l *initializationNodeLocator) Ping(ctx context.Context) error {
	l.pinged <- ctx
	<-ctx.Done()
	return ctx.Err()
}

func (l *blockingEpochRegistrationLocator) RegisterNodeEpoch(ctx context.Context, serviceName, nodeID, epoch string, ttl time.Duration) error {
	if l.blockBefore {
		close(l.registrationReached)
		<-l.releaseRegistration
	}
	err := l.Locator.RegisterNodeEpoch(ctx, serviceName, nodeID, epoch, ttl)
	if !l.blockBefore {
		close(l.registrationReached)
		<-l.releaseRegistration
	}
	return err
}

func (l *blockingEpochRegistrationLocator) UnregisterNodeEpoch(ctx context.Context, serviceName, nodeID, epoch string) error {
	if l.unregisterStarted != nil {
		close(l.unregisterStarted)
	}
	if l.releaseUnregister != nil {
		<-l.releaseUnregister
	}
	err := l.Locator.UnregisterNodeEpoch(ctx, serviceName, nodeID, epoch)
	if l.unregistered != nil {
		close(l.unregistered)
	}
	return err
}

func (l *failingEpochUnregisterLocator) UnregisterNodeEpoch(ctx context.Context, serviceName, nodeID, epoch string) error {
	l.ctx = ctx
	if l.calls.Add(1) == 1 {
		return l.err
	}
	return l.Locator.UnregisterNodeEpoch(ctx, serviceName, nodeID, epoch)
}

func (l *blockingEpochUnregisterLocator) UnregisterNodeEpoch(ctx context.Context, _, _, _ string) error {
	l.ctx = ctx
	if l.calls.Add(1) == 1 {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

func waitNodeSignal(t testing.TB, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Node lifecycle signal")
	}
}

type invalidNodeAppInfo struct {
	id   string
	name string
}

func (a invalidNodeAppInfo) ID() string { return a.id }

func (a invalidNodeAppInfo) Name() string { return a.name }

func (invalidNodeAppInfo) Version() string { return "" }

func (invalidNodeAppInfo) Metadata() map[string]string { return nil }

func (invalidNodeAppInfo) Endpoint() []string { return nil }
