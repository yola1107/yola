package pushbench

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"

	"yola/node"

	"github.com/go-kratos/kratos/v3"
	"github.com/go-kratos/kratos/v3/registry"
	"github.com/stretchr/testify/require"
)

func TestStartServerUsesNativeApplication(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	server, err := node.NewServer(node.Listener(listener), node.Registration(new(lifecycleRegistrar)))
	require.NoError(t, err)
	observed := &observedApplicationServer{Server: server, identity: make(chan kratos.AppInfo, 1)}
	startServer(t, observed, "game", "node-a", nodeReady(t, listener.Addr().String()))
	identity := <-observed.identity
	require.Equal(t, "node-a", identity.ID())
	require.Equal(t, "game", identity.Name())
	require.Empty(t, identity.Endpoint(), "the internal Node endpoint must not leak into the ordinary application record")
}

func TestStartServerRegistersAndStopsNativeApplication(t *testing.T) {
	registrar := new(lifecycleRegistrar)
	t.Run("application", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		t.Cleanup(func() { _ = listener.Close() })
		server, err := node.NewServer(node.Listener(listener), node.Registration(registrar))
		require.NoError(t, err)
		startServer(t, server, "game", "node-a", nodeReady(t, listener.Addr().String()))
		require.Equal(t, int32(1), registrar.registered.Load(), "startup returned before registration")
		require.Equal(t, "node-a", registrar.instance.ID)
		require.Equal(t, "game", registrar.instance.Name)
		require.Len(t, registrar.instance.Endpoints, 1)
	})
	require.Equal(t, int32(1), registrar.deregistered.Load(), "cleanup skipped application deregistration")
}

func TestStartServerFailureCleanup(t *testing.T) {
	if os.Getenv("YOLA_PUSHBENCH_START_FAILURE") != "" {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		t.Cleanup(func() { _ = listener.Close() })
		address := listener.Addr().String()
		registrar := new(rejectedRegistrar)
		server, err := node.NewServer(node.Listener(listener), node.Registration(registrar))
		require.NoError(t, err)
		// helper 的 cleanup 必须先于检查，不能依赖子进程退出释放 listener。
		t.Cleanup(func() {
			rebound, bindErr := net.Listen("tcp", address)
			require.NoError(t, bindErr)
			require.NoError(t, rebound.Close())
			require.False(t, registrar.deregistered.Load(), "failed registration must not deregister unknown ownership")
			t.Log("owned Node listener reclaimed")
		})
		startServer(t, server, "game", "node-a", nodeReady(t, address))
		t.Fatal("startup failure was accepted")
	}
	executable, err := os.Executable()
	require.NoError(t, err)
	command := exec.CommandContext(t.Context(), executable, "-test.run=^TestStartServerFailureCleanup$", "-test.timeout=15s", "-test.v")
	command.Env = append(os.Environ(), "YOLA_PUSHBENCH_START_FAILURE=register")
	output, childErr := command.CombinedOutput()
	require.Error(t, childErr, "the child must report the injected startup failure")
	require.Contains(t, string(output), "injected register failure")
	require.Contains(t, string(output), "owned Node listener reclaimed")
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
	registered   atomic.Int32
	deregistered atomic.Int32
	instance     *registry.ServiceInstance
}

func (r *lifecycleRegistrar) Register(_ context.Context, instance *registry.ServiceInstance) error {
	r.instance = instance
	r.registered.Add(1)
	return nil
}

func (r *lifecycleRegistrar) Deregister(context.Context, *registry.ServiceInstance) error {
	r.deregistered.Add(1)
	return nil
}

type rejectedRegistrar struct{ deregistered atomic.Bool }

func (*rejectedRegistrar) Register(context.Context, *registry.ServiceInstance) error {
	return errors.New("injected register failure")
}

func (r *rejectedRegistrar) Deregister(context.Context, *registry.ServiceInstance) error {
	r.deregistered.Store(true)
	return nil
}
