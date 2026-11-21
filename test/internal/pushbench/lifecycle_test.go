package pushbench

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"

	"yola/node"

	"github.com/go-kratos/kratos/v3"
	"github.com/go-kratos/kratos/v3/registry"
	"github.com/stretchr/testify/require"
)

func TestStartServerRegistersAndStopsNativeApplication(t *testing.T) {
	registrar := new(lifecycleRegistrar)
	t.Run("application", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		t.Cleanup(func() { _ = listener.Close() })
		server, err := node.NewServer(node.Listener(listener), node.Registration(registrar))
		require.NoError(t, err)
		observed := &observedApplicationServer{Server: server, identity: make(chan kratos.AppInfo, 1)}
		require.NoError(t, startServer(t, observed, "game", "node-a", nodeReady(t, listener.Addr().String())))

		identity := <-observed.identity
		require.Equal(t, "node-a", identity.ID())
		require.Equal(t, "game", identity.Name())
		require.Empty(t, identity.Endpoint(), "the internal Node endpoint must not leak into the ordinary application record")
		require.Equal(t, int32(1), registrar.registered.Load(), "startup returned before registration")
		require.Equal(t, "node-a", registrar.instance.ID)
		require.Equal(t, "game", registrar.instance.Name)
		require.Len(t, registrar.instance.Endpoints, 1)
	})
	require.Equal(t, int32(1), registrar.deregistered.Load(), "cleanup skipped application deregistration")
}

func TestStartServerFailureCleanup(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	address := listener.Addr().String()
	registrar := &lifecycleRegistrar{registerErr: errors.New("injected register failure")}
	server, err := node.NewServer(node.Listener(listener), node.Registration(registrar))
	require.NoError(t, err)
	t.Run("registration", func(t *testing.T) {
		require.ErrorIs(t, startServer(t, server, "game", "node-a", nodeReady(t, address)), registrar.registerErr)
	})

	rebound, err := net.Listen("tcp", address)
	require.NoError(t, err)
	require.NoError(t, rebound.Close())
	require.Zero(t, registrar.deregistered.Load(), "failed registration must not deregister unknown ownership")
}

type observedApplicationServer struct {
	*node.Server
	identity chan kratos.AppInfo
}

func (s *observedApplicationServer) Start(ctx context.Context) error {
	identity, _ := kratos.FromContext(ctx)
	s.identity <- identity
	return s.Server.Start(ctx)
}

type lifecycleRegistrar struct {
	registerErr  error
	registered   atomic.Int32
	deregistered atomic.Int32
	instance     *registry.ServiceInstance
}

func (r *lifecycleRegistrar) Register(_ context.Context, instance *registry.ServiceInstance) error {
	r.instance = instance
	r.registered.Add(1)
	return r.registerErr
}

func (r *lifecycleRegistrar) Deregister(context.Context, *registry.ServiceInstance) error {
	r.deregistered.Add(1)
	return nil
}
