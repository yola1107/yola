package env

import (
	"bytes"
	"context"
	"os"
	"runtime/pprof"
	"sync"
	"testing"
	"time"

	"github.com/go-kratos/kratos/v3/registry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.etcd.io/etcd/client/v3"
)

func TestRegistryCloseStopsKeepAlive(t *testing.T) {
	address := os.Getenv("YOLA_ETCD_INTEGRATION")
	if address == "" {
		t.Skip("set YOLA_ETCD_INTEGRATION to a disposable etcd instance")
	}
	previousAddress := EtcdAddr
	EtcdAddr = address
	t.Cleanup(func() { EtcdAddr = previousAddress })
	provider, err := NewRegistry()
	require.NoError(t, err)
	closeRegistry := sync.OnceValue(provider.Close)
	t.Cleanup(func() { require.NoError(t, closeRegistry()) })
	observer, err := clientv3.New(clientv3.Config{Endpoints: []string{address}, DialTimeout: time.Second})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, observer.Close()) })
	instance := &registry.ServiceInstance{ID: "instance-a", Name: "yola-cleanup-" + uuid.NewString()}
	key := "/microservices/" + instance.Name + "/" + instance.ID
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
	heartbeatRunning := func() bool {
		var stacks bytes.Buffer
		if err := pprof.Lookup("goroutine").WriteTo(&stacks, 2); err != nil {
			return true
		}
		return bytes.Contains(stacks.Bytes(), []byte("registry/etcd/v3.(*Registry).heartBeat"))
	}
	require.Eventually(t, heartbeatRunning, time.Second, time.Millisecond)
	require.NoError(t, closeRegistry())
	require.Eventually(t, func() bool { return !heartbeatRunning() }, 3*time.Second, 10*time.Millisecond)

	// 失败回收不主动注销，注册记录由原有 etcd lease 自然过期。
	require.Eventually(t, func() bool {
		readCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		response, err := observer.Get(readCtx, key)
		return err == nil && len(response.Kvs) == 0
	}, 20*time.Second, 100*time.Millisecond)
}
