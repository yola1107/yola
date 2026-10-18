package node

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"yola/locate"
	locateredis "yola/locate/redis"
	"yola/registry/etcd"

	"github.com/go-kratos/kratos/v3"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	clientv3 "go.etcd.io/etcd/client/v3"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

func TestRegistrationLeaseLossStopsNode(t *testing.T) {
	address := os.Getenv("YOLA_ETCD_INTEGRATION")
	if address == "" {
		t.Skip("set YOLA_ETCD_INTEGRATION to a disposable etcd instance")
	}
	for _, tc := range []struct {
		name     string
		stateful bool
	}{
		{name: "stateless"},
		{name: "stateful", stateful: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prefix := "/node-lease-test/" + uuid.NewString()
			directory, err := etcd.NewNodeRegistry(etcd.WithEndpoints(address), etcd.WithPrefix(prefix))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, directory.Close()) })
			observer, err := clientv3.New(clientv3.Config{Endpoints: []string{address}, DialTimeout: time.Second})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, observer.Close()) })
			opts := []Option{Address("127.0.0.1:0"), Registration(directory)}
			if tc.stateful {
				opts = append(opts, Stateful(newMemoryNodeStore()))
			}
			server := newTestServer(t, opts...)
			result := runTestNode(t, server)
			require.Eventually(t, func() bool {
				records, lookupErr := directory.GetService(t.Context(), "game")
				return lookupErr == nil && len(records) == 1
			}, time.Second, time.Millisecond)
			records, err := directory.GetService(t.Context(), "game")
			require.NoError(t, err)
			require.Len(t, records, 1)
			_, health := nodeClients(t, records[0])
			require.Eventually(t, func() bool { return healthStatus(t, health) == healthpb.HealthCheckResponse_SERVING }, time.Second, time.Millisecond)
			registered, err := observer.Get(t.Context(), prefix+"/", clientv3.WithPrefix())
			require.NoError(t, err)
			require.Len(t, registered.Kvs, 1)
			_, err = observer.Revoke(t.Context(), clientv3.LeaseID(registered.Kvs[0].Lease))
			require.NoError(t, err)
			select {
			case startErr := <-result:
				require.ErrorIs(t, startErr, errRegistrationLost)
			case <-time.After(10 * time.Second):
				t.Fatal("Node stayed available after registration keepalive detected lease loss")
			}
			assertNodeAddressReusable(t, records[0])
			records, err = directory.GetService(t.Context(), "game")
			require.NoError(t, err)
			require.Empty(t, records)
		})
	}
}

func TestAppPreparedNodeCannotOverwriteReplacement(t *testing.T) {
	if os.Getenv("YOLA_REDIS_INTEGRATION") == "" || os.Getenv("YOLA_ETCD_INTEGRATION") == "" {
		t.Skip("set YOLA_REDIS_INTEGRATION and YOLA_ETCD_INTEGRATION to disposable instances")
	}
	redisClient := redis.NewClient(&redis.Options{
		Addr:                  os.Getenv("YOLA_REDIS_INTEGRATION"),
		Password:              os.Getenv("YOLA_REDIS_PASSWORD"),
		DB:                    9,
		ContextTimeoutEnabled: true,
	})
	t.Cleanup(func() { require.NoError(t, redisClient.Close()) })
	store := locateredis.New(redisClient)
	service := "yola-i47-" + uuid.NewString()
	oldRegistry := integrationNodeRegistry(t)
	newRegistry := integrationNodeRegistry(t)
	prepared := make(chan struct{})
	resume := make(chan struct{})
	resumeOld := sync.OnceFunc(func() { close(resume) })
	oldStore := &controlledEpochLocator{Store: store}
	oldStore.setRenew(func(ctx context.Context) error {
		close(prepared)
		<-resume
		epoch, err := store.LocateNodeEpoch(ctx, service, "node-a")
		if err != nil {
			return err
		}
		return store.RenewNodeEpoch(ctx, service, "node-a", epoch+"-old", DefaultNodeEpochTTL)
	})
	old := newTestServer(t, Address("127.0.0.1:0"), Stateful(oldStore), Registration(oldRegistry))
	replacement := newTestServer(t, Address("127.0.0.1:0"), Stateful(store), Registration(newRegistry))
	ctx := kratos.NewContext(context.Background(), nodeTestAppInfo{id: "node-a", name: service})
	t.Cleanup(func() {
		resumeOld()
		_ = old.Stop(context.Background())
		_ = replacement.Stop(context.Background())
	})
	oldDone := make(chan error, 1)
	go func() { oldDone <- old.Start(ctx) }()
	receiveNodeValue(t, prepared)
	key := "locate:node:epoch:{" + base64.RawURLEncoding.EncodeToString([]byte(service)) + "}:" + base64.RawURLEncoding.EncodeToString([]byte("node-a"))
	require.NoError(t, redisClient.PExpire(context.Background(), key, time.Millisecond).Err())
	require.Eventually(t, func() bool {
		_, err := store.LocateNodeEpoch(context.Background(), service, "node-a")
		return errors.Is(err, locate.ErrNodeEpochNotFound)
	}, time.Second, time.Millisecond)
	startTestNode(ctx, t, replacement)
	instances, err := newRegistry.GetService(context.Background(), service)
	require.NoError(t, err)
	require.Len(t, instances, 1)
	wantEndpoint := instances[0].Endpoints[0]
	wantEpoch, err := store.LocateNodeEpoch(context.Background(), service, "node-a")
	require.NoError(t, err)
	resumeOld()
	require.ErrorIs(t, receiveNodeValue(t, oldDone), locate.ErrNodeEpochConflict)
	currentEpoch, err := store.LocateNodeEpoch(context.Background(), service, "node-a")
	require.NoError(t, err)
	require.Equal(t, wantEpoch, currentEpoch)
	instances, err = newRegistry.GetService(context.Background(), service)
	require.NoError(t, err)
	require.Len(t, instances, 1)
	require.Equal(t, []string{wantEndpoint}, instances[0].Endpoints)
}

func integrationNodeRegistry(t *testing.T) *etcd.Registry {
	t.Helper()
	provider, err := etcd.New(
		etcd.WithEndpoints(os.Getenv("YOLA_ETCD_INTEGRATION")), etcd.WithPrefix("/yola/node-registry-test"),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, provider.Close()) })
	return provider
}
