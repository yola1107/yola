package env

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/go-kratos/kratos/v3/registry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.etcd.io/etcd/client/v3"
)

func TestNewRegistryUsesConfiguredEndpointAndPrefix(t *testing.T) {
	address := os.Getenv("YOLA_ETCD_INTEGRATION")
	if address == "" {
		t.Skip("set YOLA_ETCD_INTEGRATION to a disposable etcd instance")
	}
	previousAddress := EtcdAddr
	EtcdAddr = address
	t.Cleanup(func() { EtcdAddr = previousAddress })
	provider, err := NewRegistry()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, provider.Close()) })
	observer, err := clientv3.New(clientv3.Config{Endpoints: []string{address}, DialTimeout: time.Second})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, observer.Close()) })
	instance := &registry.ServiceInstance{ID: "instance-a", Name: "yola-example-" + uuid.NewString()}
	key := "/microservices/" + instance.Name + "/" + instance.ID
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if deregisterErr := provider.Deregister(cleanupCtx, instance); deregisterErr != nil {
			t.Errorf("deregister example instance: %v", deregisterErr)
		}
		_, deleteErr := observer.Delete(cleanupCtx, key)
		require.NoError(t, deleteErr)
	})
	registerCtx, cancelRegister := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancelRegister)
	require.NoError(t, provider.Register(registerCtx, instance))
	cancelRegister()

	readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	response, err := observer.Get(readCtx, key)
	require.NoError(t, err)
	require.Len(t, response.Kvs, 1)
	var registered registry.ServiceInstance
	require.NoError(t, json.Unmarshal(response.Kvs[0].Value, &registered))
	require.Equal(t, instance, &registered)

	// 工厂必须为注册与发现配置同一个 namespace。
	instances, err := provider.GetService(readCtx, instance.Name)
	require.NoError(t, err)
	require.Equal(t, []*registry.ServiceInstance{instance}, instances)
}
