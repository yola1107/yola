package gateway

import (
	"context"
	"errors"
	"net"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yola/locate"
	"yola/node"

	"github.com/go-kratos/kratos/v3"
	"github.com/go-kratos/kratos/v3/registry"
	"github.com/go-kratos/kratos/v3/transport"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
)

func TestApplicationOwnerCleansUpKratosFailures(t *testing.T) {
	for _, kind := range []string{"gateway", "node"} {
		for _, stage := range []string{"endpoint", "before start", "register", "after start", "normal stop", "deregister"} {
			t.Run(kind+"/"+stage, func(t *testing.T) {
				stopsNormally := stage == "normal stop" || stage == "deregister"
				store := testLocator(t)
				var server transport.Server
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				require.NoError(t, err)
				if kind == "gateway" {
					server = newTestServer(t, Locator(store), Listener(listener))
				} else {
					nodeServer, nodeErr := node.NewServer(node.Registration(&testNodeRegistration{}), node.Listener(listener), node.Stateful(store))
					require.NoError(t, nodeErr)
					server = nodeServer
				}
				observed := &observedApplicationServer{
					Server:    server,
					address:   listener.Addr().String(),
					started:   make(chan struct{}),
					startDone: make(chan struct{}),
					stopDone:  make(chan struct{}),
				}
				appCtx, cancelApp := context.WithCancel(context.Background())
				cleanup := func() error {
					cancelApp()
					cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
					defer cancel()
					return server.Stop(cleanupCtx)
				}
				t.Cleanup(func() { require.NoError(t, cleanup()) })

				injectedErr := errors.New("injected application failure")
				registrar := &applicationRegistrar{started: observed.started}
				if stage == "register" {
					registrar.registerErr = injectedErr
				}
				if stage == "deregister" {
					registrar.deregisterErr = injectedErr
				}
				servers := []transport.Server{observed}
				if stage == "endpoint" {
					servers = append(servers, &failedApplicationEndpoint{err: injectedErr})
				}
				ready := make(chan struct{})
				appOptions := []kratos.Option{
					kratos.Context(appCtx),
					kratos.ID("instance-a"),
					kratos.Name(kind),
					kratos.Server(servers...),
					kratos.BeforeStart(func(context.Context) error {
						if stage == "before start" {
							return injectedErr
						}
						return nil
					}),
					kratos.AfterStart(func(context.Context) error {
						if stage == "after start" {
							return injectedErr
						}
						close(ready)
						return nil
					}),
					kratos.Registrar(registrar),
					kratos.StopTimeout(time.Second),
				}
				app := kratos.New(appOptions...)
				runDone := make(chan error, 1)
				go func() { runDone <- app.Run() }()
				if stopsNormally {
					waitApplicationSignal(t, ready)
					require.ErrorIs(t, app.Stop(), registrar.deregisterErr)
					if stage == "deregister" {
						// Kratos 在注销失败时不取消 App，所有者仍须触发并等待清理。
						require.NoError(t, cleanup())
					}
				}
				select {
				case runErr := <-runDone:
					if stopsNormally {
						require.NoError(t, runErr)
					} else {
						require.ErrorIs(t, runErr, injectedErr)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("application did not return")
				}
				require.NoError(t, cleanup())
				require.NoError(t, cleanup(), "repeated cleanup must preserve the terminal state")
				require.ErrorIs(t, appCtx.Err(), context.Canceled)

				if stage != "endpoint" && stage != "before start" {
					waitApplicationSignal(t, observed.startDone)
					waitApplicationSignal(t, observed.stopDone)
				}
				require.NotEmpty(t, observed.address)
				rebound, err := net.Listen("tcp", observed.address)
				require.NoError(t, err)
				require.NoError(t, rebound.Close())
				if kind == "node" {
					_, err = store.LocateNodeEpoch(context.Background(), kind, "instance-a")
					require.ErrorIs(t, err, locate.ErrNodeEpochNotFound)
				}
				if stopsNormally {
					require.Equal(t, int32(1), registrar.deregistered.Load())
				} else {
					require.Zero(t, registrar.deregistered.Load(), "failure cleanup must not deregister another instance")
				}
			})
		}
	}
}

func TestStartMakesInternalRPCReadyBeforeClientTransport(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	started := make(chan error, 1)
	stopped := make(chan struct{})
	release := sync.OnceFunc(func() { close(stopped) })
	gate := newTestServer(t, Listener(listener), Transport(&stubTransport{
		start: func(ctx context.Context) error {
			conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
			if err != nil {
				started <- err
				return err
			}
			defer conn.Close()
			probeCtx, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			_, err = grpc_health_v1.NewHealthClient(conn).Check(probeCtx, &grpc_health_v1.HealthCheckRequest{})
			started <- err
			<-stopped
			return nil
		},
		stop: func(context.Context) error {
			release()
			return nil
		},
	}))
	done := make(chan error, 1)
	t.Cleanup(func() {
		release()
		require.NoError(t, gate.Stop(context.Background()))
	})
	ctx := kratos.NewContext(context.Background(), testAppInfo{id: "gate-a", name: "gateway"})
	go func() { done <- gate.Start(ctx) }()
	select {
	case err := <-started:
		require.NoError(t, err)
	case err := <-done:
		t.Fatalf("Start returned before serving clients: %v", err)
	case <-time.After(time.Second):
		t.Fatal("client transport did not start")
	}
	require.NoError(t, gate.Stop(context.Background()))
	require.NoError(t, receiveWithin(t, done))
}

func TestStartUsesInternalGRPCEndpoint(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	want := &url.URL{Scheme: "grpc", Host: lis.Addr().String()}
	store := testLocator(t)
	server := newTestGateway(t, store, "grpc://127.0.0.1:1", Listener(lis), Endpoint(want))
	initTestGateway(t, server)
	conn := newTestConnection("conn-endpoint")
	require.NoError(t, server.Open(t.Context(), conn))
	_, err = server.Handle(t.Context(), conn, authMessage(t))
	require.NoError(t, err)
	binding, err := store.LocateGate(t.Context(), "game", "player-a")
	require.NoError(t, err)
	require.Equal(t, want.String(), binding.Binding.GateEndpoint)
}

func TestStartFailureReleasesInternalGRPCListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	server := newTestServer(t, Listener(listener))
	ctx := kratos.NewContext(context.Background(), testAppInfo{name: "gateway"})
	require.EqualError(t, server.Start(ctx), "gateway: application ID is required")
	rebound, err := net.Listen("tcp", address)
	require.NoError(t, err)
	require.NoError(t, rebound.Close())
	require.NoError(t, server.Stop(context.Background()))
}

func TestStartUsesCallerContext(t *testing.T) {
	pinged := make(chan context.Context, 1)
	gate := newTestServer(t, Locator(&initializationLocator{Locator: testLocator(t), pinged: pinged}))
	baseCtx, cancel := context.WithCancel(context.Background())
	ctx := kratos.NewContext(baseCtx, testAppInfo{id: "gate-a", name: "gate"})
	started := make(chan error, 1)
	go func() { started <- gate.Start(ctx) }()
	preparationCtx := receiveWithin(t, pinged)
	require.EqualError(t, gate.Start(ctx), "gateway: server is already starting or started")
	cancel()
	require.ErrorIs(t, receiveWithin(t, started), context.Canceled)
	require.ErrorIs(t, preparationCtx.Err(), context.Canceled)
	require.NoError(t, gate.Stop(context.Background()))
}

func TestStartReentryPreservesRunningServer(t *testing.T) {
	gate := newTestServer(t)
	initTestGateway(t, gate)
	require.EqualError(t, gate.Start(context.Background()), "gateway: server is already starting or started")
	conn := newTestConnection("conn-reentry")
	require.NoError(t, gate.Open(t.Context(), conn))
	require.NoError(t, gate.Stop(context.Background()))
	require.Error(t, gate.Open(t.Context(), newTestConnection("conn-after-stop")))
	require.EqualError(t, gate.Start(context.Background()), "gateway: server is stopping or stopped")
}

func TestStartLabelsClientAndGRPCTransportFailures(t *testing.T) {
	t.Run("client transport", func(t *testing.T) {
		startErr := errors.New("start failed")
		gate := newTestServer(t, Transport(&stubTransport{}, &stubTransport{start: func(context.Context) error { return startErr }}))
		ctx := kratos.NewContext(context.Background(), testAppInfo{id: "gate-a", name: "gateway"})
		err := gate.Start(ctx)
		require.ErrorIs(t, err, startErr)
		require.ErrorContains(t, err, "gateway: start client transport 1 (*gateway.stubTransport)")
		require.NoError(t, gate.Stop(context.Background()))
	})
	t.Run("gRPC transport", func(t *testing.T) {
		startErr := errors.New("accept failed")
		gate := newTestServer(t, Listener(&failingListener{acceptErr: startErr}))
		ctx := kratos.NewContext(context.Background(), testAppInfo{id: "gate-a", name: "gateway"})
		err := gate.Start(ctx)
		require.ErrorIs(t, err, startErr)
		require.ErrorContains(t, err, "gateway: start gRPC transport")
		require.NoError(t, gate.Stop(context.Background()))
	})
}

func TestGatewayPreparationFailureReleasesResources(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	gate := newTestServer(t, Listener(listener), Locator(&failingPingLocator{Locator: testLocator(t)}))
	require.EqualError(t, newTestApp(gate).Run(), "transient ping failure")
	require.NoError(t, gate.Stop(context.Background()))
	rebound, err := net.Listen("tcp", address)
	require.NoError(t, err)
	require.NoError(t, rebound.Close())
}

func TestGatewayTransportPreparationFailureClosesEveryResource(t *testing.T) {
	prepareErr := errors.New("transport preparation failed")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	var cleanupErr error
	prepared := &stubTransport{
		stop: func(ctx context.Context) error {
			cleanupErr = ctx.Err()
			return nil
		},
	}
	failing := &stubTransport{
		prepareErr: prepareErr,
		stop: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}
	gate := newTestServer(t, Listener(listener), CleanupTimeout(20*time.Millisecond), Transport(prepared, failing))
	err = newTestApp(gate).Run()
	require.ErrorIs(t, err, prepareErr)
	require.ErrorContains(t, err, "gateway: prepare client transport 1 (*gateway.stubTransport)")
	require.ErrorContains(t, err, "gateway: roll back client transport 1 (*gateway.stubTransport)")
	require.NoError(t, cleanupErr)
	require.ErrorIs(t, gate.Stop(context.Background()), context.DeadlineExceeded)
	require.Error(t, gate.Open(t.Context(), newTestConnection("conn-after-preparation-failure")))
	rebound, err := net.Listen("tcp", address)
	require.NoError(t, err)
	require.NoError(t, rebound.Close())
}

func waitApplicationSignal(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("application background task did not finish")
	}
}

type observedApplicationServer struct {
	transport.Server
	address   string
	started   chan struct{}
	startDone chan struct{}
	stopDone  chan struct{}
	stopOnce  sync.Once
}

func (s *observedApplicationServer) Start(ctx context.Context) error {
	close(s.started)
	defer close(s.startDone)
	return s.Server.Start(ctx)
}

func (s *observedApplicationServer) Stop(ctx context.Context) error {
	defer s.stopOnce.Do(func() { close(s.stopDone) })
	return s.Server.Stop(ctx)
}

type failedApplicationEndpoint struct {
	transport.Server
	err error
}

func (s *failedApplicationEndpoint) Endpoint() (*url.URL, error) { return nil, s.err }

type applicationRegistrar struct {
	started       <-chan struct{}
	registerErr   error
	deregisterErr error
	deregistered  atomic.Int32
}

func (r *applicationRegistrar) Register(ctx context.Context, _ *registry.ServiceInstance) error {
	select {
	case <-r.started:
		return r.registerErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *applicationRegistrar) Deregister(context.Context, *registry.ServiceInstance) error {
	r.deregistered.Add(1)
	return r.deregisterErr
}

type initializationLocator struct {
	locate.Locator
	pinged chan<- context.Context
}

type failingPingLocator struct {
	locate.Locator
}

func (s *initializationLocator) Ping(ctx context.Context) error {
	s.pinged <- ctx
	<-ctx.Done()
	return ctx.Err()
}

func (*failingPingLocator) Ping(context.Context) error { return errors.New("transient ping failure") }
