package node

import (
	"context"
	"errors"
	"net"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"yola/api/cluster/v1"
	"yola/instance"
	"yola/internal/clusterroute"
	"yola/locate"

	"github.com/go-kratos/kratos/v3"
	"github.com/go-kratos/kratos/v3/registry"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

func TestStartValidatesApplicationIdentity(t *testing.T) {
	for _, app := range []nodeTestAppInfo{{id: "node-a"}, {name: "game"}} {
		server := newTestServer(t)
		require.EqualError(t, server.Start(kratos.NewContext(context.Background(), app)), "node: invalid application identity")
		require.NoError(t, server.Stop(context.Background()))
	}
	server := newTestServer(t)
	require.EqualError(t, server.Start(context.Background()), "node: application context is required")
	require.NoError(t, server.Stop(context.Background()))
}

func TestConstructionHasNoListenerOrEpochSideEffects(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())
	store := newMemoryNodeStore()
	server := newTestServer(t, Address(address), Stateful(store))
	rebound, err := net.Listen("tcp", address)
	require.NoError(t, err)
	require.NoError(t, rebound.Close())
	_, err = store.LocateNodeEpoch(context.Background(), "game", "node-a")
	require.ErrorIs(t, err, locate.ErrNodeEpochNotFound)
	require.NoError(t, server.Stop(context.Background()))
	require.EqualError(t, server.Start(testNodeContext()), "node: server is stopping or stopped")
}

func TestRegistrationKeepsRPCAndHealthClosedUntilSuccess(t *testing.T) {
	registrar := newLifecycleRegistrar()
	release := make(chan struct{})
	resume := sync.OnceFunc(func() { close(release) })
	registrar.register = func(context.Context, *registry.ServiceInstance) error {
		<-release
		return nil
	}
	server := newTestServer(t, Address("127.0.0.1:0"), Registration(registrar))
	server.RegisterRawHandler(1, func(context.Context, []byte) ([]byte, error) { return []byte("ready"), nil })
	done := make(chan error, 1)
	go func() { done <- server.Start(testNodeContext()) }()
	t.Cleanup(func() {
		resume()
		_ = server.Stop(context.Background())
	})
	record := receiveNodeValue(t, registrar.entered)
	client, health := nodeClients(t, record)
	require.Equal(t, healthpb.HealthCheckResponse_NOT_SERVING, healthStatus(t, health))
	_, err := client.Forward(context.Background(), &v1.ForwardRequest{Route: clusterroute.FromBinding(testBinding("player-a", "conn-a")), Command: 1})
	require.Equal(t, codes.Unavailable, status.Code(err))
	resume()
	require.Eventually(t, func() bool { return healthStatus(t, health) == healthpb.HealthCheckResponse_SERVING }, time.Second, time.Millisecond)
	reply, err := client.Forward(context.Background(), &v1.ForwardRequest{Route: clusterroute.FromBinding(testBinding("player-a", "conn-a")), Command: 1})
	require.NoError(t, err)
	require.Equal(t, []byte("ready"), reply.Body)
	require.NoError(t, server.Stop(context.Background()))
	require.NoError(t, receiveNodeValue(t, done))
	require.Equal(t, int32(1), registrar.deregistered.Load())
}

func TestRegistrationDerivesOnlyInternalNodeRecord(t *testing.T) {
	for _, stateful := range []bool{false, true} {
		t.Run(map[bool]string{false: "stateless", true: "stateful"}[stateful], func(t *testing.T) {
			registrar := newLifecycleRegistrar()
			opts := []Option{Address("127.0.0.1:0"), Registration(registrar)}
			if stateful {
				opts = append(opts, Stateful(newMemoryNodeStore()))
			}
			server := newTestServer(t, opts...)
			ctx := kratos.NewContext(context.Background(), nodeTestAppInfo{
				id:       "node-a",
				name:     "game",
				metadata: map[string]string{"business": "metadata", instance.StickyMetadataKey: "invalid"},
			})
			startTestNode(ctx, t, server)
			record := registrar.instance()
			require.Equal(t, "node-a", record.ID)
			require.Equal(t, "game", record.Name)
			require.Len(t, record.Endpoints, 1)
			endpoint, err := url.Parse(record.Endpoints[0])
			require.NoError(t, err)
			require.Equal(t, "grpc", endpoint.Scheme)
			if stateful {
				require.Equal(t, instance.StickyMetadata(), record.Metadata)
			} else {
				require.Empty(t, record.Metadata)
			}
			assertHandlerRegistrationFrozen(t, server)
			require.EqualError(t, server.Start(ctx), "node: server is already started")
		})
	}
}

func TestRegistrationWaitsForInitialEpochRenewal(t *testing.T) {
	store := &controlledEpochLocator{Store: newMemoryNodeStore()}
	renewing := make(chan struct{})
	release := make(chan struct{})
	resume := sync.OnceFunc(func() { close(release) })
	store.setRenew(func(ctx context.Context) error {
		close(renewing)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	registrar := newLifecycleRegistrar()
	server := newTestServer(t, Address("127.0.0.1:0"), Stateful(store), Registration(registrar))
	done := make(chan error, 1)
	go func() { done <- server.Start(testNodeContext()) }()
	t.Cleanup(func() {
		resume()
		_ = server.Stop(context.Background())
	})
	receiveNodeValue(t, renewing)
	select {
	case <-registrar.entered:
		t.Fatal("Node registered before first epoch renewal")
	default:
	}
	resume()
	receiveNodeValue(t, registrar.entered)
	require.NoError(t, server.Stop(context.Background()))
	require.NoError(t, receiveNodeValue(t, done))
}

func TestInitialRenewalFailurePreservesCause(t *testing.T) {
	failure := errors.New("first renewal failed")
	store := &controlledEpochLocator{Store: newMemoryNodeStore()}
	store.setRenew(func(context.Context) error { return failure })
	registrar := newLifecycleRegistrar()
	server := newTestServer(t, Stateful(store), Registration(registrar))
	require.ErrorIs(t, server.Start(testNodeContext()), failure)
	require.NoError(t, server.Stop(context.Background()))
	select {
	case <-registrar.entered:
		t.Fatal("Node registered with failed first renewal")
	default:
	}
	_, err := store.LocateNodeEpoch(context.Background(), "game", "node-a")
	require.ErrorIs(t, err, locate.ErrNodeEpochNotFound)
}

func TestLateRegistrationHasOneCleanupOwner(t *testing.T) {
	registrar := newLifecycleRegistrar()
	release := make(chan struct{})
	resume := sync.OnceFunc(func() { close(release) })
	registrar.register = func(context.Context, *registry.ServiceInstance) error {
		<-release
		return nil
	}
	store := newMemoryNodeStore()
	server := newTestServer(t, Address("127.0.0.1:0"), Stateful(store), Registration(registrar), ShutdownTimeout(80*time.Millisecond))
	done := make(chan error, 1)
	go func() { done <- server.Start(testNodeContext()) }()
	t.Cleanup(func() {
		resume()
		_ = server.Stop(context.Background())
	})
	record := receiveNodeValue(t, registrar.entered)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, server.Stop(ctx), context.DeadlineExceeded)
	// 登记忽略取消时，已知网络也必须被有界清理。
	require.Eventually(t, func() bool {
		endpoint := server.grpcListener.Addr().String()
		return tcpAddressReusable(endpoint)
	}, time.Second, time.Millisecond)
	resume()
	require.Error(t, receiveNodeValue(t, done))
	_ = server.Stop(context.Background())
	require.Equal(t, int32(1), registrar.deregistered.Load())
	_, err := store.LocateNodeEpoch(context.Background(), "game", "node-a")
	require.ErrorIs(t, err, locate.ErrNodeEpochNotFound)
	assertNodeAddressReusable(t, record)
}

func TestLateRegistrationIsReclaimedBeforeLateDrainReturns(t *testing.T) {
	registrar := newLifecycleRegistrar()
	registrationReleased := make(chan struct{})
	resumeRegistration := sync.OnceFunc(func() { close(registrationReleased) })
	registrar.register = func(context.Context, *registry.ServiceInstance) error {
		<-registrationReleased
		return nil
	}
	drainExpired := make(chan struct{})
	drainReleased := make(chan struct{})
	resumeDrain := sync.OnceFunc(func() { close(drainReleased) })
	store := newMemoryNodeStore()
	server := newTestServer(t, Address("127.0.0.1:0"), Stateful(store), Registration(registrar),
		ShutdownTimeout(80*time.Millisecond), Drain(func(ctx context.Context) error {
			<-ctx.Done()
			close(drainExpired)
			<-drainReleased
			return nil
		}))
	started := make(chan error, 1)
	go func() { started <- server.Start(testNodeContext()) }()
	t.Cleanup(func() {
		resumeRegistration()
		resumeDrain()
		_ = server.Stop(context.Background())
	})
	receiveNodeValue(t, registrar.entered)
	stopped := make(chan error, 1)
	go func() { stopped <- server.Stop(context.Background()) }()
	receiveNodeValue(t, drainExpired)
	resumeRegistration()
	require.Eventually(t, func() bool { return registrar.deregistered.Load() == 1 }, time.Second, time.Millisecond,
		"late Drain must not block cleanup of a completed registration")
	_, err := store.LocateNodeEpoch(context.Background(), "game", "node-a")
	require.NoError(t, err, "incomplete drain must leave epoch expiry to TTL")
	resumeDrain()
	require.ErrorIs(t, receiveNodeValue(t, stopped), context.DeadlineExceeded)
	require.Error(t, receiveNodeValue(t, started))
	require.Equal(t, int32(1), registrar.deregistered.Load())
}

func TestRegistrationRechecksEpochBeforeAdmission(t *testing.T) {
	store := newMemoryNodeStore()
	registrar := newLifecycleRegistrar()
	server := newTestServer(t, Stateful(store), Registration(registrar))
	// Register 的替身只控制存储失权；回调在租约已发布后执行。
	registrar.register = func(context.Context, *registry.ServiceInstance) error {
		lease := server.lease.Load()
		lease.cancel(locate.ErrNodeEpochConflict)
		return nil
	}
	require.ErrorIs(t, server.Start(testNodeContext()), locate.ErrNodeEpochConflict)
	require.True(t, server.requests.isClosed())
	require.Equal(t, int32(1), registrar.deregistered.Load())
}

func TestRequestAdmissionStopClosesEmptyAdmission(t *testing.T) {
	var admission requestAdmission
	require.False(t, admission.admit())
	require.NoError(t, admission.stopAndWait(context.Background()))
	admission.open()
	require.False(t, admission.admit())
	require.NoError(t, admission.stopAndWait(context.Background()))
}

func TestRequestAdmissionTimeoutKeepsAcceptedWorkTracked(t *testing.T) {
	var admission requestAdmission
	admission.open()
	require.True(t, admission.admit())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	require.ErrorIs(t, admission.stopAndWait(ctx), context.Canceled)
	require.False(t, admission.admit())
	admission.done()
	require.NoError(t, admission.stopAndWait(ctx))
	require.False(t, admission.admit())
}

func TestStopCallerTimeoutCanKeepWaitingAndDrainRunsOnce(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	resume := sync.OnceFunc(func() { close(release) })
	var drains atomic.Int32
	server := newTestServer(t, Drain(func(context.Context) error {
		drains.Add(1)
		close(entered)
		<-release
		return nil
	}))
	t.Cleanup(resume)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	stopped := make(chan error, 1)
	go func() { stopped <- server.Stop(ctx) }()
	receiveNodeValue(t, entered)
	require.ErrorIs(t, receiveNodeValue(t, stopped), context.DeadlineExceeded)
	resume()
	require.NoError(t, server.Stop(context.Background()))
	require.NoError(t, server.Stop(context.Background()))
	require.Equal(t, int32(1), drains.Load())
}

func TestDeregisterFailureStillDrainsAndReleasesResources(t *testing.T) {
	for _, block := range []bool{false, true} {
		t.Run(map[bool]string{false: "error", true: "timeout"}[block], func(t *testing.T) {
			registrar := newLifecycleRegistrar()
			failure := errors.New("directory unavailable")
			registrar.deregister = func(ctx context.Context, _ *registry.ServiceInstance) error {
				if block {
					<-ctx.Done()
					return ctx.Err()
				}
				return failure
			}
			var drains atomic.Int32
			store := newMemoryNodeStore()
			server := newTestServer(
				t,
				Address("127.0.0.1:0"),
				Stateful(store),
				Registration(registrar),
				ShutdownTimeout(200*time.Millisecond),
				Drain(func(context.Context) error {
					drains.Add(1)
					return nil
				}),
			)
			startTestNode(testNodeContext(), t, server)
			record := registrar.instance()
			err := server.Stop(context.Background())
			if block {
				require.ErrorIs(t, err, context.DeadlineExceeded)
			} else {
				require.ErrorIs(t, err, failure)
			}
			require.Equal(t, int32(1), drains.Load())
			_, err = store.LocateNodeEpoch(context.Background(), "game", "node-a")
			require.ErrorIs(t, err, locate.ErrNodeEpochNotFound)
			assertNodeAddressReusable(t, record)
		})
	}
}

func TestStopClosesHealthBeforeBusinessDrain(t *testing.T) {
	registrar := newLifecycleRegistrar()
	drainEntered := make(chan struct{})
	resume := make(chan struct{})
	release := sync.OnceFunc(func() { close(resume) })
	server := newTestServer(
		t,
		Address("127.0.0.1:0"),
		Registration(registrar),
		Drain(func(context.Context) error {
			close(drainEntered)
			<-resume
			return nil
		}),
	)
	startTestNode(testNodeContext(), t, server)
	client, health := nodeClients(t, registrar.instance())
	stopped := make(chan error, 1)
	go func() { stopped <- server.Stop(context.Background()) }()
	t.Cleanup(release)
	receiveNodeValue(t, drainEntered)
	require.Equal(t, healthpb.HealthCheckResponse_NOT_SERVING, healthStatus(t, health))
	_, err := client.Forward(context.Background(), &v1.ForwardRequest{Route: clusterroute.FromBinding(testBinding("player-a", "conn-a")), Command: 1})
	require.Equal(t, codes.Unavailable, status.Code(err))
	release()
	require.NoError(t, receiveNodeValue(t, stopped))
}

func TestStartupRegistrationFailurePreservesCauseAndCleansEpoch(t *testing.T) {
	registrar := newLifecycleRegistrar()
	failure := errors.New("register failed")
	registrar.register = func(context.Context, *registry.ServiceInstance) error { return failure }
	store := newMemoryNodeStore()
	server := newTestServer(t, Address("127.0.0.1:0"), Registration(registrar), Stateful(store), PushTimeout(time.Nanosecond))
	require.ErrorIs(t, server.Start(testNodeContext()), failure)
	require.NoError(t, server.Stop(context.Background()))
	_, err := store.LocateNodeEpoch(context.Background(), "game", "node-a")
	require.ErrorIs(t, err, locate.ErrNodeEpochNotFound)
	assertNodeAddressReusable(t, registrar.instance())
}

func TestOwnedDrainTimeoutKeepsEpochUntilTTL(t *testing.T) {
	store := newMemoryNodeStore()
	registrar := newLifecycleRegistrar()
	server := newTestServer(
		t,
		Address("127.0.0.1:0"),
		Stateful(store),
		Registration(registrar),
		ShutdownTimeout(20*time.Millisecond),
		Drain(func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		}),
	)
	startTestNode(testNodeContext(), t, server)
	require.ErrorIs(t, server.Stop(context.Background()), context.DeadlineExceeded)
	require.ErrorIs(t, server.Stop(context.Background()), context.DeadlineExceeded)
	_, err := store.LocateNodeEpoch(context.Background(), "game", "node-a")
	require.NoError(t, err)
	assertNodeAddressReusable(t, registrar.instance())
}

func TestShutdownClosesNetworkBeforeLateDrainReturns(t *testing.T) {
	store := newMemoryNodeStore()
	registrar := newLifecycleRegistrar()
	expired := make(chan struct{})
	release := make(chan struct{})
	resume := sync.OnceFunc(func() { close(release) })
	server := newTestServer(t, Address("127.0.0.1:0"), Stateful(store), Registration(registrar),
		ShutdownTimeout(20*time.Millisecond), Drain(func(ctx context.Context) error {
			<-ctx.Done()
			close(expired)
			<-release
			return ctx.Err()
		}))
	startTestNode(testNodeContext(), t, server)
	t.Cleanup(resume)
	stopped := make(chan error, 1)
	go func() { stopped <- server.Stop(context.Background()) }()
	receiveNodeValue(t, expired)
	endpoint, err := url.Parse(registrar.instance().Endpoints[0])
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return tcpAddressReusable(endpoint.Host)
	}, time.Second, time.Millisecond, "late Drain must not keep the owned listener open")
	_, err = store.LocateNodeEpoch(context.Background(), "game", "node-a")
	require.NoError(t, err, "incomplete drain must leave epoch expiry to TTL")
	select {
	case stopErr := <-stopped:
		t.Fatalf("Stop returned before Drain handed back ownership: %v", stopErr)
	default:
	}
	resume()
	require.ErrorIs(t, receiveNodeValue(t, stopped), context.DeadlineExceeded)
}

func TestServerTransportTimeoutRecoveryAndRepeatedStop(t *testing.T) {
	registrar := newLifecycleRegistrar()
	server := newTestServer(t, Address("127.0.0.1:0"), Registration(registrar), HandlerTimeout(20*time.Millisecond))
	server.RegisterRawHandler(1, func(ctx context.Context, _ []byte) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	server.RegisterRawHandler(2, func(context.Context, []byte) ([]byte, error) { panic("failed") })
	startTestNode(testNodeContext(), t, server)
	client, _ := nodeClients(t, registrar.instance())
	request := &v1.ForwardRequest{Route: clusterroute.FromBinding(testBinding("player-a", "conn-a")), Command: 1}
	_, err := client.Forward(context.Background(), request)
	require.Equal(t, codes.DeadlineExceeded, status.Code(err))
	request.Command = 2
	_, err = client.Forward(context.Background(), request)
	require.Equal(t, codes.Internal, status.Code(err))
	require.NoError(t, server.Stop(context.Background()))
	require.NoError(t, server.Stop(context.Background()))
}

func TestStopDuringInitialRenewalReclaimsEpoch(t *testing.T) {
	store := &controlledEpochLocator{Store: newMemoryNodeStore()}
	entered := make(chan struct{})
	store.setRenew(func(ctx context.Context) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	})
	registrar := newLifecycleRegistrar()
	server := newTestServer(t, Address("127.0.0.1:0"), Stateful(store), Registration(registrar))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, server.Stop(ctx))
	})
	done := make(chan error, 1)
	go func() { done <- server.Start(testNodeContext()) }()
	receiveNodeValue(t, entered)
	assertHandlerRegistrationFrozen(t, server)
	require.EqualError(t, server.Start(testNodeContext()), "node: server is already starting")
	require.NoError(t, server.Stop(context.Background()))
	require.ErrorIs(t, receiveNodeValue(t, done), context.Canceled)
	select {
	case <-registrar.entered:
		t.Fatal("stopped Node registered while first renewal was pending")
	default:
	}
	_, err := store.LocateNodeEpoch(context.Background(), "game", "node-a")
	require.ErrorIs(t, err, locate.ErrNodeEpochNotFound)
}

func TestStopReclaimsLateEpochClaimWithOriginalCredentials(t *testing.T) {
	base := newMemoryNodeStore()
	entered := make(chan struct{})
	release := make(chan struct{})
	resume := sync.OnceFunc(func() { close(release) })
	store := &lateEpochLocator{Store: base, entered: entered, release: release}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	server := newTestServer(t, Listener(listener), Stateful(store), ShutdownTimeout(80*time.Millisecond))
	done := make(chan error, 1)
	go func() { done <- server.Start(testNodeContext()) }()
	t.Cleanup(func() {
		resume()
		_ = server.Stop(context.Background())
	})
	receiveNodeValue(t, entered)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, server.Stop(ctx), context.DeadlineExceeded)
	require.Eventually(t, func() bool {
		return tcpAddressReusable(address)
	}, time.Second, time.Millisecond)
	resume()
	require.ErrorIs(t, receiveNodeValue(t, done), context.Canceled)
	_ = server.Stop(context.Background())
	_, err = base.LocateNodeEpoch(context.Background(), "game", "node-a")
	require.ErrorIs(t, err, locate.ErrNodeEpochNotFound)
	require.NoError(t, base.RegisterNodeEpoch(context.Background(), "game", "node-a", "replacement", DefaultNodeEpochTTL))
}

func TestDrainKeepsStartApplicationIdentity(t *testing.T) {
	server := newTestServer(t, Address("127.0.0.1:0"), Drain(func(ctx context.Context) error {
		app, ok := kratos.FromContext(ctx)
		if !ok || app.Name() != "game" || app.ID() != "node-a" {
			return errors.New("Drain lost application identity")
		}
		return ctx.Err()
	}))
	startTestNode(testNodeContext(), t, server)
	require.NoError(t, server.Stop(context.Background()))
}

func TestDefaultShutdownBudgetSurvivesShortCallerWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var budget time.Duration
		server := newTestServer(t, Drain(func(ctx context.Context) error {
			app, ok := kratos.FromContext(ctx)
			if !ok || app.Name() != "game" {
				return errors.New("Drain lost Stop application context")
			}
			deadline, ok := ctx.Deadline()
			if !ok {
				return errors.New("Drain has no owned deadline")
			}
			budget = time.Until(deadline)
			time.Sleep(4 * time.Second)
			return ctx.Err()
		}))
		ctx, cancel := context.WithTimeout(testNodeContext(), 100*time.Millisecond)
		defer cancel()
		require.ErrorIs(t, server.Stop(ctx), context.DeadlineExceeded)
		require.NoError(t, server.Stop(context.Background()))
		require.Equal(t, 10*time.Second, budget)
	})
}

func TestStartupRollbackHasIndependentBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var budget time.Duration
		server := newTestServer(t, PushTimeout(time.Nanosecond), Drain(func(ctx context.Context) error {
			deadline, ok := ctx.Deadline()
			if !ok {
				return errors.New("Drain has no rollback deadline")
			}
			budget = time.Until(deadline)
			<-ctx.Done()
			return ctx.Err()
		}))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := server.Start(kratos.NewContext(ctx, nodeTestAppInfo{id: "node-a"}))
		require.ErrorContains(t, err, "node: invalid application identity")
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.ErrorIs(t, server.Stop(context.Background()), context.DeadlineExceeded)
		require.Equal(t, 3*time.Second, budget)
	})
}

func TestServerStartFailureClosesResources(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	require.NoError(t, listener.Close())
	store := newMemoryNodeStore()
	registrar := newLifecycleRegistrar()
	server := newTestServer(t, Listener(listener), Stateful(store), Registration(registrar))
	app := kratos.New(kratos.ID("node-a"), kratos.Name("game"), kratos.Server(server))
	require.Error(t, app.Run())
	require.NoError(t, server.Stop(context.Background()))
	_, err = store.LocateNodeEpoch(context.Background(), "game", "node-a")
	require.ErrorIs(t, err, locate.ErrNodeEpochNotFound)
	_, err = server.forward(context.Background(), testBinding("player-a", "conn-a"), 1, nil)
	requireNodeDraining(t, err)
}

func healthStatus(t *testing.T, client healthpb.HealthClient) healthpb.HealthCheckResponse_ServingStatus {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	reply, err := client.Check(ctx, &healthpb.HealthCheckRequest{}, grpc.WaitForReady(true))
	require.NoError(t, err)
	return reply.Status
}

func assertNodeAddressReusable(t *testing.T, record *registry.ServiceInstance) {
	t.Helper()
	endpoint, err := url.Parse(record.Endpoints[0])
	require.NoError(t, err)
	listener, err := net.Listen("tcp", endpoint.Host)
	require.NoError(t, err)
	require.NoError(t, listener.Close())
}

type lateEpochLocator struct {
	Store
	entered chan struct{}
	release <-chan struct{}
}

func (l *lateEpochLocator) RegisterNodeEpoch(ctx context.Context, service, nodeID, epoch string, ttl time.Duration) error {
	if err := l.Store.RegisterNodeEpoch(ctx, service, nodeID, epoch, ttl); err != nil {
		return err
	}
	close(l.entered)
	<-l.release
	return nil
}
