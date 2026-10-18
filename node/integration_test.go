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
)

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
