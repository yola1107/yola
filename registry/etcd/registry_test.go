package etcd

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/go-kratos/kratos/v3/registry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc"
)

func TestRegistryProtectsReplacementOwnership(t *testing.T) {
	client := integrationClient(t)
	namespace := "/yola/registry-test/" + uuid.NewString()
	old := newRegistry(client, namespace)
	service := &registry.ServiceInstance{ID: "node-a", Name: "game", Endpoints: []string{"grpc://127.0.0.1:10001"}}
	ctx := t.Context()
	require.NoError(t, old.Register(ctx, service))
	t.Cleanup(func() { require.NoError(t, old.Deregister(context.Background(), service)) })

	conflicting := newRegistry(client, namespace)
	require.ErrorIs(t, conflicting.Register(ctx, service), ErrInstanceExists)
	require.NoError(t, conflicting.Deregister(ctx, service))
	instances, err := old.GetService(ctx, service.Name)
	require.NoError(t, err)
	require.Equal(t, []*registry.ServiceInstance{service}, instances)

	_, err = client.Revoke(ctx, old.leaseID)
	require.NoError(t, err)
	select {
	case <-old.session.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("lost registration lease did not stop its worker")
	}
	replacement := newRegistry(client, namespace)
	newService := &registry.ServiceInstance{ID: service.ID, Name: service.Name, Endpoints: []string{"grpc://127.0.0.1:10002"}}
	require.NoError(t, replacement.Register(ctx, newService))
	t.Cleanup(func() { require.NoError(t, replacement.Deregister(context.Background(), newService)) })
	require.NoError(t, old.Deregister(ctx, service))
	instances, err = replacement.GetService(ctx, service.Name)
	require.NoError(t, err)
	require.Equal(t, []*registry.ServiceInstance{newService}, instances)
	require.Error(t, old.Register(ctx, service), "lost ownership must not permit re-registration")
	require.NoError(t, replacement.Deregister(ctx, newService))
	instances, err = replacement.GetService(ctx, service.Name)
	require.NoError(t, err)
	require.Empty(t, instances)
}

func TestDelayedRegistrationCannotOverwriteReplacement(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	releaseRegister := sync.OnceFunc(func() { close(release) })
	oldClient := integrationClient(t, grpc.WithUnaryInterceptor(func(
		ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption,
	) error {

		if method == "/etcdserverpb.KV/Txn" {
			close(entered)
			<-release
		}
		return invoker(ctx, method, req, reply, cc, opts...)
	}))
	t.Cleanup(releaseRegister)
	newClient := integrationClient(t)
	namespace := "/yola/registry-test/" + uuid.NewString()
	old := newRegistry(oldClient, namespace)
	replacement := newRegistry(newClient, namespace)
	service := &registry.ServiceInstance{ID: "node-a", Name: "game"}
	registered := make(chan error, 1)
	go func() { registered <- old.Register(t.Context(), service) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("registration did not reach transaction")
	}
	oldLease := old.leaseID
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, old.Deregister(ctx, service), context.Canceled, "waiting for registration must honor cancellation")
	require.NoError(t, replacement.Register(t.Context(), service))
	t.Cleanup(func() { require.NoError(t, replacement.Deregister(context.Background(), service)) })
	releaseRegister()
	require.ErrorIs(t, <-registered, ErrInstanceExists)
	lease, err := newClient.TimeToLive(t.Context(), oldLease)
	require.NoError(t, err)
	require.Negative(t, lease.TTL, "failed registration must revoke its own lease")
	require.NoError(t, old.Deregister(t.Context(), service))
	instances, err := replacement.GetService(t.Context(), service.Name)
	require.NoError(t, err)
	require.Equal(t, []*registry.ServiceInstance{service}, instances)
}

func TestRegistrationCanceledAfterCommitRollsBackOwnLease(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := integrationClient(t, grpc.WithUnaryInterceptor(func(
		callCtx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption,
	) error {

		err := invoker(callCtx, method, req, reply, cc, opts...)
		if method == "/etcdserverpb.KV/Txn" && err == nil {
			cancel()
			return ctx.Err()
		}
		return err
	}))
	provider := newRegistry(client, "/yola/registry-test/"+uuid.NewString())
	service := &registry.ServiceInstance{ID: "node-a", Name: "game"}
	require.ErrorIs(t, provider.Register(ctx, service), context.Canceled)
	instances, err := provider.GetService(t.Context(), service.Name)
	require.NoError(t, err)
	require.Empty(t, instances)
	require.Zero(t, provider.leaseID)
	require.NoError(t, provider.Deregister(t.Context(), service))
}

func TestRegistryClientCloseStopsLeaseWorker(t *testing.T) {
	client := integrationClient(t)
	provider := newRegistry(client, "/yola/registry-test/"+uuid.NewString())
	registerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service := &registry.ServiceInstance{ID: "node-a", Name: "game"}
	require.NoError(t, provider.Register(registerCtx, service))
	cancel()
	select {
	case <-provider.session.Done():
		t.Fatal("register context cancellation stopped lifetime keepalive")
	default:
	}
	require.NoError(t, client.Close())
	select {
	case <-provider.session.Done():
	case <-time.After(time.Second):
		t.Fatal("client Close left registration worker running")
	}
}

func integrationClient(t *testing.T, opts ...grpc.DialOption) *clientv3.Client {
	t.Helper()
	endpoint := os.Getenv("YOLA_ETCD_INTEGRATION")
	if endpoint == "" {
		t.Skip("set YOLA_ETCD_INTEGRATION to a disposable etcd instance")
	}
	client, err := clientv3.New(clientv3.Config{
		Endpoints: []string{endpoint}, DialTimeout: time.Second, DialOptions: opts,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		if client.Ctx().Err() == nil {
			require.NoError(t, client.Close())
		}
	})
	return client
}
