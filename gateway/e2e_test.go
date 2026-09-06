package gateway

import (
	"context"
	"encoding/base64"
	"os"
	"testing"
	"time"

	"yola/api/protocol/v1"
	"yola/instance"
	"yola/locate"
	locateredis "yola/locate/redis"
	"yola/node"

	"github.com/go-kratos/kratos/contrib/registry/etcd/v3"
	"github.com/go-kratos/kratos/v3/registry"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"go.etcd.io/etcd/client/v3"
)

func TestGatewayNodeIntegration(t *testing.T) {
	redisAddress := os.Getenv("YOLA_REDIS_INTEGRATION")
	etcdAddress := os.Getenv("YOLA_ETCD_INTEGRATION")
	if redisAddress == "" || etcdAddress == "" {
		t.Skip("set YOLA_REDIS_INTEGRATION and YOLA_ETCD_INTEGRATION to run Gate/Node integration test")
	}
	if redisAddress == "1" {
		redisAddress = "127.0.0.1:6379"
	}
	if etcdAddress == "1" {
		etcdAddress = "127.0.0.1:2379"
	}

	redisClient := redis.NewClient(&redis.Options{
		Addr:     redisAddress,
		Password: os.Getenv("YOLA_REDIS_PASSWORD"),
	})
	t.Cleanup(func() { require.NoError(t, redisClient.Close()) })
	require.NoError(t, redisClient.Ping(context.Background()).Err())
	store := locateredis.New(redisClient)

	etcdClient, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{etcdAddress},
		DialTimeout: 2 * time.Second,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, etcdClient.Close()) })
	statusCtx, statusCancel := context.WithTimeout(context.Background(), 2*time.Second)
	_, err = etcdClient.Status(statusCtx, etcdAddress)
	statusCancel()
	require.NoError(t, err)

	suffix := uuid.NewString()
	discovery := etcd.New(etcdClient, etcd.Namespace("/yola/e2e/"+suffix))
	serviceName := "yola-test-e2e-" + suffix
	nodeID := "node-" + suffix
	endpoint, stopNode := startTestNodeServer(t, nodeID, serviceName, true, node.Locator(store))
	nodeInstance := &registry.ServiceInstance{
		ID: nodeID, Name: serviceName, Metadata: instance.StickyMetadata(),
		Endpoints: []string{endpoint},
	}
	registerCtx, registerCancel := context.WithTimeout(context.Background(), 3*time.Second)
	require.NoError(t, discovery.Register(registerCtx, nodeInstance))
	registerCancel()

	tag := base64.RawURLEncoding.EncodeToString([]byte(serviceName + "\x00player-a"))
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cleanupCancel()
		require.NoError(t, discovery.Deregister(cleanupCtx, nodeInstance))
		require.NoError(t, redisClient.Del(cleanupCtx, "locate:gate:{"+tag+"}").Err())
		require.NoError(t, redisClient.Del(cleanupCtx, "locate:node:{"+tag+"}").Err())
		stopNode()
	})

	require.Eventually(t, func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		instances, getErr := discovery.GetService(ctx, serviceName)
		return getErr == nil && len(instances) == 1 && instances[0].ID == nodeID
	}, 3*time.Second, 20*time.Millisecond)

	gate := newTestServer(t,
		Auth(testAuthenticator{}),
		Locator(store),
		Discovery(discovery),
		LeaseTTL(time.Minute),
	)
	initTestGateway(t, gate)
	conn := newTestConnection("conn-" + suffix)
	require.NoError(t, gate.Open(context.Background(), conn))
	_, err = gate.Handle(context.Background(), conn, authMessageForService(t, serviceName))
	require.NoError(t, err)

	lease, err := store.LocateGate(context.Background(), serviceName, "player-a")
	require.NoError(t, err)
	require.Equal(t, conn.ConnID(), lease.Binding.ConnID)

	request := &v1.Proto{Op: v1.OpRequest, Seq: 7, Cmd: 3, Body: []byte("request")}
	_, err = gate.Handle(context.Background(), conn, request)
	require.NoError(t, err)
	require.Equal(t, v1.OpResponse, request.Op)
	require.Equal(t, []byte("node:request"), request.Body)
	boundNodeID, err := store.LocateNode(context.Background(), serviceName, "player-a")
	require.NoError(t, err)
	require.Equal(t, nodeID, boundNodeID)

	heartbeat := &v1.Proto{Op: v1.OpHeartbeat, Seq: 8}
	_, err = gate.Handle(context.Background(), conn, heartbeat)
	require.NoError(t, err)
	require.Equal(t, v1.OpHeartbeatReply, heartbeat.Op)

	replacement := newTestConnection("replacement-" + suffix)
	require.NoError(t, gate.Open(context.Background(), replacement))
	_, err = gate.Handle(context.Background(), replacement, authMessageForService(t, serviceName))
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		select {
		case <-conn.closed:
			return true
		default:
			return false
		}
	}, time.Second, time.Millisecond)
	lease, err = store.LocateGate(context.Background(), serviceName, "player-a")
	require.NoError(t, err)
	require.Equal(t, replacement.ConnID(), lease.Binding.ConnID)
	request = &v1.Proto{Op: v1.OpRequest, Seq: 9, Cmd: 1, Body: []byte("reconnect")}
	_, err = gate.Handle(context.Background(), replacement, request)
	require.NoError(t, err)
	require.Equal(t, []byte("node:reconnect"), request.Body)

	gate.Close(context.Background(), replacement)
	_, err = store.LocateGate(context.Background(), serviceName, "player-a")
	require.ErrorIs(t, err, locate.ErrGateNotFound)
	boundNodeID, err = store.LocateNode(context.Background(), serviceName, "player-a")
	require.NoError(t, err)
	require.Equal(t, nodeID, boundNodeID)
}
