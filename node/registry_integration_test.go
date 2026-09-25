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
	redisClient := redis.NewClient(&redis.Options{Addr: os.Getenv("YOLA_REDIS_INTEGRATION"), DB: 9})
	t.Cleanup(func() { require.NoError(t, redisClient.Close()) })
	store := locateredis.New(redisClient)
	service := "yola-i47-" + uuid.NewString()
	oldRegistry := integrationNodeRegistry(t)
	newRegistry := integrationNodeRegistry(t)
	old := newTestServer(t, Address("127.0.0.1:0"), Locator(store))
	replacement := newTestServer(t, Address("127.0.0.1:0"), Locator(store))
	appCtx, cancelApp := context.WithCancel(context.Background())
	prepared := make(chan struct{})
	resume := make(chan struct{})
	resumeOld := sync.OnceFunc(func() { close(resume) })

	oldApp := kratos.New(
		kratos.Context(appCtx), kratos.ID("node-a"), kratos.Name(service), kratos.Metadata(old.Metadata()),
		kratos.BeforeStart(old.BeforeStart), kratos.BeforeStart(func(context.Context) error {
			close(prepared)
			<-resume
			return nil
		}),
		kratos.Server(old),
		kratos.Registrar(old.Registrar(oldRegistry)),
		kratos.StopTimeout(time.Second),
	)
	newRegistered := make(chan struct{})
	newApp := kratos.New(
		kratos.Context(appCtx), kratos.ID("node-a"), kratos.Name(service), kratos.Metadata(replacement.Metadata()),
		kratos.BeforeStart(replacement.BeforeStart), kratos.Server(replacement), kratos.Registrar(replacement.Registrar(newRegistry)),
		kratos.AfterStart(func(context.Context) error { close(newRegistered); return nil }),
		kratos.StopTimeout(time.Second),
	)
	t.Cleanup(func() {
		resumeOld()
		cancelApp()
		require.NoError(t, old.Stop(context.Background()))
		require.NoError(t, replacement.Stop(context.Background()))
	})
	oldDone := make(chan error, 1)
	go func() { oldDone <- oldApp.Run() }()
	waitNodeSignal(t, prepared)
	key := "locate:node:epoch:{" + base64.RawURLEncoding.EncodeToString([]byte(service)) + "}:" + base64.RawURLEncoding.EncodeToString([]byte("node-a"))
	require.NoError(t, redisClient.PExpire(context.Background(), key, time.Millisecond).Err())
	require.Eventually(t, func() bool {
		_, err := store.LocateNodeEpoch(context.Background(), service, "node-a")
		return errors.Is(err, locate.ErrNodeEpochNotFound)
	}, time.Second, time.Millisecond)
	newDone := make(chan error, 1)
	go func() { newDone <- newApp.Run() }()
	waitNodeSignal(t, newRegistered)
	newEpoch := replacement.currentIdentity().epoch
	newEndpoint, err := replacement.Endpoint()
	require.NoError(t, err)
	resumeOld()
	require.ErrorIs(t, receiveNodeValue(t, oldDone), locate.ErrNodeEpochConflict)
	currentEpoch, err := store.LocateNodeEpoch(context.Background(), service, "node-a")
	require.NoError(t, err)
	require.Equal(t, newEpoch, currentEpoch)
	instances, err := newRegistry.GetService(context.Background(), service)
	require.NoError(t, err)
	require.Len(t, instances, 1)
	gotEndpoints := instances[0].Endpoints
	cancelApp()
	require.NoError(t, receiveNodeValue(t, newDone))
	require.Equal(t, []string{newEndpoint.String()}, gotEndpoints, "old preparation overwrote the replacement registration")
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
