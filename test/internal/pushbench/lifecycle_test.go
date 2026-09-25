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
	server, err := node.NewServer(node.Address("127.0.0.1:0"))
	require.NoError(t, err)
	observed := &observedApplicationServer{Server: server}
	startServer(t, observed, "game", "node-a", nil, nil)
	require.Equal(t, "node-a", observed.identity.ID())
	require.Equal(t, "game", observed.identity.Name())
	require.NotEmpty(t, observed.endpoints, "BeforeStart must observe the instance built by real App.Run")
}

func TestStartServerRegistersAndStopsNativeApplication(t *testing.T) {
	registrar := new(lifecycleRegistrar)
	t.Run("application", func(t *testing.T) {
		server, err := node.NewServer(node.Address("127.0.0.1:0"))
		require.NoError(t, err)
		startServer(t, server, "game", "node-a", nil, server.Registrar(registrar))
		require.Equal(t, int32(1), registrar.registered.Load(), "startup returned before registration")
		require.Equal(t, "node-a", registrar.instance.ID)
		require.Equal(t, "game", registrar.instance.Name)
		require.Len(t, registrar.instance.Endpoints, 1)
	})
	require.Equal(t, int32(1), registrar.deregistered.Load(), "cleanup skipped application deregistration")
}

func TestStartServerFailureCleanup(t *testing.T) {
	if stage := os.Getenv("YOLA_PUSHBENCH_START_FAILURE"); stage != "" {
		server, err := node.NewServer(node.Address("127.0.0.1:0"))
		require.NoError(t, err)
		endpoint, err := server.Endpoint()
		require.NoError(t, err)
		registrar := new(rejectedRegistrar)
		// helper 的 cleanup 必须先于本检查执行，不能依靠子进程退出释放 listener。
		t.Cleanup(func() {
			rebound, bindErr := net.Listen("tcp", endpoint.Host)
			require.NoError(t, bindErr)
			require.NoError(t, rebound.Close())
			require.False(t, registrar.deregistered.Load(), "failed registration must not deregister unknown ownership")
			t.Log("owned Node listener reclaimed")
		})
		switch stage {
		case "prepare":
			startServer(t, &failedPreparation{Server: server}, "game", "node-a", nil, nil)
		case "register":
			startServer(t, server, "game", "node-a", nil, server.Registrar(registrar))
		default:
			t.Fatal("unknown failure stage")
		}
		t.Fatal("startup failure was accepted")
	}
	executable, err := os.Executable()
	require.NoError(t, err)
	for _, stage := range []string{"prepare", "register"} {
		t.Run(stage, func(t *testing.T) {
			command := exec.CommandContext(t.Context(), executable, "-test.run=^TestStartServerFailureCleanup$", "-test.timeout=15s", "-test.v")
			command.Env = append(os.Environ(), "YOLA_PUSHBENCH_START_FAILURE="+stage)
			output, childErr := command.CombinedOutput()
			require.Error(t, childErr, "the child must report the injected startup failure")
			require.Contains(t, string(output), "injected "+stage+" failure")
			require.Contains(t, string(output), "owned Node listener reclaimed")
		})
	}
}

type observedApplicationServer struct {
	*node.Server
	identity  kratos.AppInfo
	endpoints []string
}

func (s *observedApplicationServer) BeforeStart(ctx context.Context) error {
	s.identity, _ = kratos.FromContext(ctx)
	s.endpoints = append([]string(nil), s.identity.Endpoint()...)
	return s.Server.BeforeStart(ctx)
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

type failedPreparation struct{ *node.Server }

func (s *failedPreparation) BeforeStart(ctx context.Context) error {
	if err := s.Server.BeforeStart(ctx); err != nil {
		return err
	}
	return errors.New("injected prepare failure")
}

type rejectedRegistrar struct{ deregistered atomic.Bool }

func (*rejectedRegistrar) Register(context.Context, *registry.ServiceInstance) error {
	return errors.New("injected register failure")
}

func (r *rejectedRegistrar) Deregister(context.Context, *registry.ServiceInstance) error {
	r.deregistered.Store(true)
	return nil
}
