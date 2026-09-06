package gateway

import (
	"context"
	"errors"
	"net"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"yola/locate"
	"yola/node"

	"github.com/go-kratos/kratos/v3"
	"github.com/go-kratos/kratos/v3/registry"
	"github.com/go-kratos/kratos/v3/transport"
	"github.com/stretchr/testify/require"
)

func TestApplicationOwnerCleansUpKratosFailures(t *testing.T) {
	for _, kind := range []string{"gateway", "node"} {
		for _, stage := range []string{"endpoint", "before start", "register", "after start", "normal stop", "deregister"} {
			t.Run(kind+"/"+stage, func(t *testing.T) {
				store := testLocator(t)
				var server applicationServer
				var metadata map[string]string
				if kind == "gateway" {
					server = newTestServer(t, Locator(store))
				} else {
					nodeServer, err := node.NewServer(node.Address("127.0.0.1:0"), node.Locator(store))
					require.NoError(t, err)
					server, metadata = nodeServer, nodeServer.Metadata()
				}
				observed := &observedApplicationServer{
					applicationServer: server,
					started:           make(chan struct{}),
					startDone:         make(chan struct{}),
					stopDone:          make(chan struct{}),
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
				app := kratos.New(
					kratos.Context(appCtx),
					kratos.ID("instance-a"),
					kratos.Name(kind),
					kratos.Metadata(metadata),
					kratos.Server(servers...),
					kratos.BeforeStart(server.BeforeStart),
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
				)
				runDone := make(chan error, 1)
				go func() { runDone <- app.Run() }()
				if stage == "normal stop" || stage == "deregister" {
					waitApplicationSignal(t, ready)
					require.ErrorIs(t, app.Stop(), registrar.deregisterErr)
					if stage == "deregister" {
						// Kratos 在注销失败时不取消 App，所有者仍须触发并等待清理。
						require.NoError(t, cleanup())
					}
				}
				select {
				case runErr := <-runDone:
					if stage == "normal stop" || stage == "deregister" {
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
				if stage == "normal stop" || stage == "deregister" {
					require.Equal(t, int32(1), registrar.deregistered.Load())
				} else {
					require.Zero(t, registrar.deregistered.Load(), "failure cleanup must not deregister another instance")
				}
			})
		}
	}
}

func waitApplicationSignal(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("application background task did not finish")
	}
}

type applicationServer interface {
	transport.Server
	transport.Endpointer
	BeforeStart(context.Context) error
}

type observedApplicationServer struct {
	applicationServer
	address   string
	started   chan struct{}
	startDone chan struct{}
	stopDone  chan struct{}
}

func (s *observedApplicationServer) Endpoint() (*url.URL, error) {
	endpoint, err := s.applicationServer.Endpoint()
	if err == nil {
		s.address = endpoint.Host
	}
	return endpoint, err
}

func (s *observedApplicationServer) Start(ctx context.Context) error {
	close(s.started)
	defer close(s.startDone)
	return s.applicationServer.Start(ctx)
}

func (s *observedApplicationServer) Stop(ctx context.Context) error {
	defer close(s.stopDone)
	return s.applicationServer.Stop(ctx)
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
