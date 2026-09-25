package node

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"yola/locate"

	"github.com/go-kratos/kratos/v3"
	"github.com/go-kratos/kratos/v3/registry"
	"github.com/stretchr/testify/require"
)

func TestAppRegistrationWaitsForInitialRenewal(t *testing.T) {
	for _, stopBeforeReady := range []bool{false, true} {
		name := "ready"
		if stopBeforeReady {
			name = "stopped"
		}
		t.Run(name, func(t *testing.T) {
			store := &controlledEpochLocator{Locator: newMemoryLocator()}
			renewing := make(chan struct{})
			release := make(chan struct{})
			releaseRenewal := sync.OnceFunc(func() { close(release) })
			store.setRenew(func(ctx context.Context) error {
				close(renewing)
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
			server := newTestServer(t, Address("127.0.0.1:0"), Locator(store))
			registrar := new(countingRegistrar)
			registering := make(chan struct{})
			guarded := &observedRegistrar{Registrar: server.Registrar(registrar), registering: registering}
			appCtx, cancel := context.WithCancel(context.Background())
			app := kratos.New(
				kratos.Context(appCtx), kratos.ID("node-a"), kratos.Name("game"),
				kratos.Metadata(server.Metadata()), kratos.BeforeStart(server.BeforeStart),
				kratos.Server(server), kratos.Registrar(guarded), kratos.StopTimeout(time.Second),
			)
			t.Cleanup(func() {
				releaseRenewal()
				cancel()
				require.NoError(t, server.Stop(context.Background()))
			})
			done := make(chan error, 1)
			go func() { done <- app.Run() }()
			waitNodeSignal(t, renewing)
			waitNodeSignal(t, registering)
			require.Zero(t, registrar.registered.Load())
			if stopBeforeReady {
				require.NoError(t, server.Stop(context.Background()))
				require.Error(t, receiveNodeValue(t, done))
				require.Zero(t, registrar.registered.Load())
				return
			}
			releaseRenewal()
			require.Eventually(t, func() bool { return registrar.registered.Load() == 1 }, time.Second, time.Millisecond)
			require.NoError(t, app.Stop())
			require.NoError(t, receiveNodeValue(t, done))
		})
	}
}

func TestAppRegistrationPreservesInitialRenewalError(t *testing.T) {
	renewErr := errors.New("initial renewal failed")
	store := &controlledEpochLocator{Locator: newMemoryLocator()}
	store.setRenew(func(context.Context) error { return renewErr })
	server := newTestServer(t, Address("127.0.0.1:0"), Locator(store))
	registrar := new(countingRegistrar)
	appCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	t.Cleanup(func() { require.NoError(t, server.Stop(context.Background())) })
	app := kratos.New(
		kratos.Context(appCtx), kratos.ID("node-a"), kratos.Name("game"), kratos.Metadata(server.Metadata()),
		kratos.BeforeStart(server.BeforeStart), kratos.Server(server), kratos.Registrar(server.Registrar(registrar)),
		kratos.StopTimeout(time.Second),
	)

	require.ErrorIs(t, app.Run(), renewErr)
	cancel()
	require.NoError(t, server.Stop(context.Background()))
	require.Zero(t, registrar.registered.Load())
	_, err := store.LocateNodeEpoch(context.Background(), "game", "node-a")
	require.ErrorIs(t, err, locate.ErrNodeEpochNotFound)
}

func TestAppRegistrationFailureAllowsOwnedResourceCleanup(t *testing.T) {
	store := newMemoryLocator()
	server := newTestServer(t, Address("127.0.0.1:0"), Locator(store))
	endpoint, err := server.Endpoint()
	require.NoError(t, err)
	registerErr := errors.New("registration failed")
	registrar := &observedRegistrar{err: registerErr}
	appCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	app := kratos.New(
		kratos.Context(appCtx), kratos.ID("node-a"), kratos.Name("game"), kratos.Metadata(server.Metadata()),
		kratos.BeforeStart(server.BeforeStart), kratos.Server(server), kratos.Registrar(server.Registrar(registrar)),
		kratos.StopTimeout(time.Second),
	)

	require.ErrorIs(t, app.Run(), registerErr)
	// 与入口一致：Kratos 早退后由应用所有者取消 context，再回收 Server。
	cancel()
	ctx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	require.NoError(t, server.Stop(ctx))
	_, err = store.LocateNodeEpoch(context.Background(), "game", "node-a")
	require.ErrorIs(t, err, locate.ErrNodeEpochNotFound)
	listener, err := net.Listen("tcp", endpoint.Host)
	require.NoError(t, err)
	require.NoError(t, listener.Close())
}

func TestStopWaitsForAdmittedRegistration(t *testing.T) {
	server := newTestServer(t, Locator(newMemoryLocator()))
	ctx := kratos.NewContext(context.Background(), nodeTestAppInfo{metadata: server.Metadata()})
	require.NoError(t, server.BeforeStart(ctx))
	// 控制到达就绪的时刻，隔离已经进入注册器的 I/O 与 Stop 交错。
	server.publishReady(nil)
	entered := make(chan struct{})
	release := make(chan struct{})
	releaseRegistration := sync.OnceFunc(func() { close(release) })
	registrar := server.Registrar(&observedRegistrar{registering: entered, release: release})
	t.Cleanup(func() { require.NoError(t, server.Stop(context.Background())) })
	t.Cleanup(releaseRegistration)
	registered := make(chan error, 1)
	go func() {
		registered <- registrar.Register(context.Background(), &registry.ServiceInstance{ID: "node-a", Name: "game"})
	}()
	waitNodeSignal(t, entered)
	stopped := make(chan error, 1)
	go func() { stopped <- server.Stop(context.Background()) }()
	require.Eventually(t, server.requests.isClosed, time.Second, time.Millisecond)
	_, err := server.locator.LocateNodeEpoch(context.Background(), "game", "node-a")
	require.NoError(t, err)
	select {
	case err := <-stopped:
		t.Fatalf("Stop returned during registration: %v", err)
	default:
	}
	releaseRegistration()
	require.NoError(t, receiveNodeValue(t, registered))
	require.NoError(t, receiveNodeValue(t, stopped))
}

type observedRegistrar struct {
	registry.Registrar
	registering chan struct{}
	release     <-chan struct{}
	err         error
}

func (r *observedRegistrar) Register(ctx context.Context, service *registry.ServiceInstance) error {
	if r.registering != nil {
		close(r.registering)
	}
	if r.release != nil {
		<-r.release
	}
	if r.Registrar != nil {
		return r.Registrar.Register(ctx, service)
	}
	return r.err
}
