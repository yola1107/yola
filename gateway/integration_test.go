package gateway

import (
	"context"
	"encoding/base64"
	"errors"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"

	v1 "yola/api/protocol/v1"
	"yola/locate"
	locateredis "yola/locate/redis"
	"yola/network/tcp"
	"yola/network/websocket"
	"yola/node"
	"yola/push"
	"yola/registry/etcd"

	"github.com/go-kratos/kratos/v3"
	"github.com/go-kratos/kratos/v3/registry"
	kgrpc "github.com/go-kratos/kratos/v3/transport/grpc"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/balancer/roundrobin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/interop/grpc_testing"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestGatewayNodeIntegration(t *testing.T) {
	store, newDirectory, _, serviceName := integrationDependencies(t)
	discovery := newDirectory()
	suffix := uuid.NewString()
	nodeID := "node-" + suffix
	startTestNodeServer(t, nodeID, serviceName, node.Stateful(store), node.Registration(discovery))

	require.Eventually(t, func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		instances, getErr := discovery.GetService(ctx, serviceName)
		return getErr == nil && len(instances) == 1 && instances[0].ID == nodeID
	}, 3*time.Second, 20*time.Millisecond)

	gate := newTestServer(t,
		Locator(store),
		Discovery(discovery),
		LeaseTTL(time.Minute),
	)
	initTestGateway(t, gate)
	conn := newTestConnection("conn-" + suffix)
	require.NoError(t, gate.Open(context.Background(), conn))
	_, err := gate.Handle(context.Background(), conn, authMessageForService(t, serviceName))
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
	require.Eventually(t, isClosed(conn.closed), time.Second, time.Millisecond)
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

func TestDistributedGatewaysKeepNodeOwnershipAndIndependentSender(t *testing.T) {
	for _, transportName := range []string{"tcp", "websocket"} {
		t.Run(transportName, func(t *testing.T) {
			store, newDirectory, newOrdinaryDirectory, serviceName := integrationDependencies(t)
			ordinaryDirectory := newOrdinaryDirectory()
			discovery := newDirectory()
			sessions := make(chan distributedSession, 2)
			calls := map[string]*atomic.Int32{"node-a": {}, "node-b": {}}
			stopNode := make(map[string]func())
			endpoints := make(map[string]string)
			for _, id := range []string{"node-a", "node-b"} {
				endpoints[id], stopNode[id] = startConfiguredTestNode(t, id, serviceName, func(server *node.Server) {
					server.RegisterRawHandler(1, func(ctx context.Context, _ []byte) ([]byte, error) {
						session, _ := node.FromContext(ctx)
						if err := session.BindNode(ctx); err != nil {
							return nil, err
						}
						sessions <- distributedSession{nodeID: id, session: session}
						return []byte(id), nil
					})
					server.RegisterRawHandler(2, func(context.Context, []byte) ([]byte, error) {
						calls[id].Add(1)
						return []byte(id), nil
					})
				}, node.Stateful(store), node.Registration(newDirectory()))
			}
			records, err := discovery.GetService(t.Context(), serviceName)
			require.NoError(t, err)
			require.Len(t, records, 2)
			for _, record := range records {
				require.Equal(t, []string{endpoints[record.ID]}, record.Endpoints)
			}
			gateA := startDistributedGateway(t, transportName, "gate-a", store, discovery)
			gateB := startDistributedGateway(t, transportName, "gate-b", store, discovery)
			originalPushes := make(chan []byte, 4)
			original := dialLifecycleClient(t, transportName, gateA, serviceName, originalPushes, nil)
			ownerID := requestDistributedNode(t, original, 1)
			originalSession := receiveWithin(t, sessions)
			require.Equal(t, ownerID, originalSession.nodeID)
			require.NoError(t, originalSession.session.Push(t.Context(), 9, wrapperspb.String("original connection")))
			requirePushString(t, originalPushes, "original connection")

			currentPushes := make(chan []byte, 4)
			current := dialLifecycleClient(t, transportName, gateB, serviceName, currentPushes, nil)
			require.Equal(t, ownerID, requestDistributedNode(t, current, 2), "跨 Gateway 重连必须回到已有 Node")
			lease, err := store.LocateGate(t.Context(), serviceName, "player-a")
			require.NoError(t, err)
			require.Equal(t, "gate-b", lease.Binding.GateID)
			boundID, err := store.LocateNode(t.Context(), serviceName, "player-a")
			require.NoError(t, err)
			require.Equal(t, ownerID, boundID)
			require.Eventually(t, func() bool {
				pushErr := originalSession.session.Push(t.Context(), 9, wrapperspb.String("stale session"))
				return status.Code(pushErr) == codes.NotFound
			}, time.Second, time.Millisecond, "原 Session 不能重定向到新连接")

			notifierName := "notifier-" + uuid.NewString()
			rpc := startOrdinarySenderService(t, ordinaryDirectory, store, notifierName, serviceName)
			assertOrdinaryPush(t, rpc, currentPushes, notifierName)
			ordinaryRecords, err := ordinaryDirectory.GetService(t.Context(), serviceName)
			require.NoError(t, err)
			require.Empty(t, ordinaryRecords, "普通业务目录不能发现内部 Node")
			nodeRecords, err := discovery.GetService(t.Context(), notifierName)
			require.NoError(t, err)
			require.Empty(t, nodeRecords, "Node 目录不能发现普通业务 RPC")

			survivorID := "node-a"
			if ownerID == survivorID {
				survivorID = "node-b"
			}
			survivorCalls := calls[survivorID].Load()
			stopNode[ownerID]()
			_, code, err := current.Request(t.Context(), 2, &emptypb.Empty{})
			require.NoError(t, err)
			// 发现摘除与 epoch 释放独立传播；两种结果都须拒绝改投其他 Node。
			require.Contains(t, []int32{int32(codes.Unavailable), int32(codes.Aborted)}, code)
			require.Equal(t, survivorCalls, calls[survivorID].Load())
			boundID, err = store.LocateNode(t.Context(), serviceName, "player-a")
			require.NoError(t, err)
			require.Equal(t, ownerID, boundID)
			// Sender 只依赖 GateBinding；目标 Node 退出后普通服务仍可推送。
			assertOrdinaryPush(t, rpc, currentPushes, notifierName)
		})
	}
}

type distributedSession struct {
	nodeID  string
	session node.Session
}

func integrationDependencies(t *testing.T) (locate.Locator, func() *etcd.Registry, func() *etcd.Registry, string) {
	t.Helper()
	redisAddress, etcdAddress := os.Getenv("YOLA_REDIS_INTEGRATION"), os.Getenv("YOLA_ETCD_INTEGRATION")
	if redisAddress == "" || etcdAddress == "" {
		t.Skip("set YOLA_REDIS_INTEGRATION and YOLA_ETCD_INTEGRATION to disposable instances")
	}
	if redisAddress == "1" {
		redisAddress = "127.0.0.1:6379"
	}
	if etcdAddress == "1" {
		etcdAddress = "127.0.0.1:2379"
	}
	client := redis.NewClient(&redis.Options{Addr: redisAddress, Password: os.Getenv("YOLA_REDIS_PASSWORD"), ContextTimeoutEnabled: true})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	require.NoError(t, client.Ping(t.Context()).Err())
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
	serviceName := "yola-test-" + suffix
	prefix := "/yola/integration-test/" + suffix
	newDirectory := func() *etcd.Registry {
		directory, err := etcd.NewNodeRegistry(etcd.WithEndpoints(etcdAddress), etcd.WithPrefix(prefix))
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, directory.Close()) })
		return directory
	}
	newOrdinaryDirectory := func() *etcd.Registry {
		ordinary, err := etcd.New(etcd.WithEndpoints(etcdAddress), etcd.WithPrefix(prefix))
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, ordinary.Close()) })
		return ordinary
	}
	tag := base64.RawURLEncoding.EncodeToString([]byte(serviceName + "\x00player-a"))
	nodeKey := "locate:node:{" + base64.RawURLEncoding.EncodeToString([]byte(serviceName)) + "}:" + base64.RawURLEncoding.EncodeToString([]byte("player-a"))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, client.Del(ctx, "locate:gate:{"+tag+"}").Err())
		require.NoError(t, client.Del(ctx, nodeKey).Err())
	})
	return locateredis.New(client), newDirectory, newOrdinaryDirectory, serviceName
}

func startDistributedGateway(t *testing.T, transportName, id string, store locate.Locator, discovery registry.Discovery) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	var clientTransport ClientTransport
	if transportName == "tcp" {
		clientTransport = tcp.NewServer(tcp.Listener(listener))
	} else {
		clientTransport = websocket.NewServer(websocket.Listener(listener))
	}
	gateway := newTestServer(t, Address("127.0.0.1:0"), Locator(store), Discovery(discovery), Transport(clientTransport), RPCTimeout(time.Second), LeaseTTL(time.Minute))
	startNamedTestApp(t, gateway, id)
	return listener.Addr().String()
}

func requestDistributedNode(t *testing.T, client lifecycleClient, command int32) string {
	t.Helper()
	body, code, err := client.Request(t.Context(), command, &emptypb.Empty{})
	require.NoError(t, err)
	require.Equal(t, int32(codes.OK), code)
	return string(body)
}

func requirePushString(t *testing.T, pushes <-chan []byte, want string) {
	t.Helper()
	var message wrapperspb.StringValue
	require.NoError(t, proto.Unmarshal(receiveWithin(t, pushes), &message))
	require.Equal(t, want, message.Value)
}

type ordinarySenderService struct {
	grpc_testing.UnimplementedTestServiceServer
	sender     *push.Sender
	targetGame string
}

func (s *ordinarySenderService) UnaryCall(ctx context.Context, _ *grpc_testing.SimpleRequest) (*grpc_testing.SimpleResponse, error) {
	app, ok := kratos.FromContext(ctx)
	if !ok {
		return nil, errors.New("ordinary RPC lost application identity")
	}
	if app.Name() == s.targetGame {
		return nil, errors.New("ordinary RPC must have its own service identity")
	}
	if err := s.sender.PushToUID(ctx, s.targetGame, "player-a", 9, wrapperspb.String("notice from "+app.Name())); err != nil {
		return nil, err
	}
	return &grpc_testing.SimpleResponse{Username: app.Name()}, nil
}

func startOrdinarySenderService(t *testing.T, directory *etcd.Registry, store locate.Locator, serviceName, targetGame string) grpc_testing.TestServiceClient {
	t.Helper()
	sender, err := push.NewSender(store)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sender.Close()) })
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := kgrpc.NewServer(kgrpc.Listener(listener), kgrpc.Address(listener.Addr().String()))
	grpc_testing.RegisterTestServiceServer(server, &ordinarySenderService{sender: sender, targetGame: targetGame})
	app := kratos.New(kratos.ID("notifier-a"), kratos.Name(serviceName), kratos.Server(server), kratos.Registrar(directory), kratos.StopTimeout(3*time.Second))
	done := make(chan error, 1)
	t.Cleanup(func() {
		require.NoError(t, app.Stop())
		require.NoError(t, receiveWithin(t, done))
		require.NoError(t, server.Stop(context.Background()))
	})
	go func() { done <- app.Run() }()
	require.Eventually(t, func() bool {
		records, lookupErr := directory.GetService(t.Context(), serviceName)
		return lookupErr == nil && len(records) == 1
	}, 3*time.Second, 10*time.Millisecond)
	conn, err := kgrpc.NewClient(
		t.Context(),
		kgrpc.WithEndpoint("discovery:///"+serviceName),
		kgrpc.WithDiscovery(directory),
		kgrpc.WithTimeout(time.Second),
		kgrpc.WithOptions(grpc.WithDefaultServiceConfig(`{"loadBalancingConfig":[{"`+roundrobin.Name+`":{}}]}`)),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	return grpc_testing.NewTestServiceClient(conn)
}

func assertOrdinaryPush(t *testing.T, client grpc_testing.TestServiceClient, pushes <-chan []byte, serviceName string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	reply, err := client.UnaryCall(ctx, &grpc_testing.SimpleRequest{}, grpc.WaitForReady(true))
	require.NoError(t, err)
	require.Equal(t, serviceName, reply.Username)
	requirePushString(t, pushes, "notice from "+serviceName)
}
