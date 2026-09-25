package etcd

import (
	"context"
	"crypto/rand"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/go-kratos/kratos/v3/registry"
	"github.com/stretchr/testify/require"
	"go.etcd.io/etcd/client/v3"
)

func TestWithEndpointsSplitsAndTrims(t *testing.T) {
	registry, err := New(WithEndpoints("127.0.0.1:2379, 127.0.0.2:2379"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer registry.Close()

	want := []string{"127.0.0.1:2379", "127.0.0.2:2379"}
	if got := registry.client.Endpoints(); !slices.Equal(got, want) {
		t.Fatalf("Endpoints() = %v, want %v", got, want)
	}
}

func TestNewRejectsEmptyEndpoints(t *testing.T) {
	if _, err := New(WithEndpoints(" , ")); err == nil {
		t.Fatal("New() accepted empty endpoints")
	}
}

func TestRegistryCloseStopsKeepAlive(t *testing.T) {
	address := os.Getenv("YOLA_ETCD_INTEGRATION")
	if address == "" {
		t.Skip("set YOLA_ETCD_INTEGRATION to a disposable etcd instance")
	}
	prefix := "/yola/app-cleanup/" + rand.Text()
	provider, err := New(WithEndpoints(address), WithPrefix(prefix))
	require.NoError(t, err)
	closeRegistry := sync.OnceValue(provider.Close)
	t.Cleanup(func() { require.NoError(t, closeRegistry()) })
	observer, err := clientv3.New(clientv3.Config{Endpoints: []string{address}, DialTimeout: time.Second})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, observer.Close()) })
	instance := &registry.ServiceInstance{ID: "instance-a", Name: "game"}
	key := prefix + "/" + instance.Name + "/" + instance.ID
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, err := observer.Delete(cleanupCtx, key)
		require.NoError(t, err)
	})
	registerCtx, cancelRegister := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancelRegister)
	require.NoError(t, provider.Register(registerCtx, instance))
	cancelRegister()
	require.NoError(t, closeRegistry())
	require.ErrorIs(t, provider.client.Ctx().Err(), context.Canceled)

	// 失败回收不主动注销，注册记录由原有 etcd lease 自然过期。
	require.Eventually(t, func() bool {
		readCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		response, err := observer.Get(readCtx, key)
		return err == nil && len(response.Kvs) == 0
	}, 20*time.Second, 100*time.Millisecond)
}
