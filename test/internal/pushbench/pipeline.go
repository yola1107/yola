package pushbench

import (
	"context"
	"errors"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"yola/api/protocol/v1"
	"yola/gateway"
	"yola/locate"
	locateredis "yola/locate/redis"
	"yola/node"

	"github.com/go-kratos/kratos/v3"
	"github.com/go-kratos/kratos/v3/registry"
	"github.com/go-kratos/kratos/v3/transport"
	kgrpc "github.com/go-kratos/kratos/v3/transport/grpc"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// Pusher 是桌广播基准所需的客户端投递能力。
type Pusher interface {
	PushToUID(context.Context, string, int32, proto.Message) error
}

type measuredPusher struct {
	server       *node.Server
	measurements *measurements
}

func (p *measuredPusher) PushToUID(ctx context.Context, uid string, command int32, message proto.Message) error {
	started := time.Now()
	err := p.server.PushToUID(ctx, uid, command, message)
	p.measurements.record("push", started, err)
	return err
}

type measuredLocator struct {
	locate.Locator
	measurements *measurements
}

func (s *measuredLocator) LocateGate(ctx context.Context, service, uid string) (locate.GateLease, error) {
	started := time.Now()
	lease, err := s.Locator.LocateGate(ctx, service, uid)
	s.measurements.record("locate_gate", started, err)
	return lease, err
}

func newPipeline(t testing.TB, address, service string, playerCount int, delay time.Duration, measurements *measurements) (Pusher, *atomic.Uint64) {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: address})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	store := locateredis.New(client)
	gate, err := gateway.NewServer(
		gateway.Address("127.0.0.1:0"), gateway.Auth(benchmarkAuthenticator{}),
		gateway.Locator(store), gateway.Discovery(unusedDiscovery{}), gateway.LeaseTTL(time.Hour),
	)
	require.NoError(t, err)
	startServer(t, gate, "gateway", service+"-gateway", nil)
	server, err := node.NewServer(
		node.Address("127.0.0.1:0"),
		node.Locator(&measuredLocator{Locator: store, measurements: measurements}),
		node.ClientMiddleware(measurements.rpc),
	)
	require.NoError(t, err)
	startServer(t, server, service, service+"-node", server.Metadata())
	received := new(atomic.Uint64)
	for playerID := 1; playerID <= playerCount; playerID++ {
		uid := strconv.Itoa(playerID)
		connection := &benchmarkConnection{id: uid, received: received, delay: delay}
		require.NoError(t, gate.Open(context.Background(), connection))
		body, err := proto.Marshal(&v1.ClientAuthReq{ServiceName: service, Token: []byte(uid)})
		require.NoError(t, err)
		reply, err := gate.Handle(context.Background(), connection, &v1.Proto{Op: v1.OpAuth, Body: body})
		require.NoError(t, err)
		require.Zero(t, reply.Code, "benchmark client authentication failed")
	}
	pusher := &measuredPusher{server: server, measurements: measurements}
	// 在计时前完成连接、路由和 protobuf 首次调用，确认投递确实到达真实 Gateway。
	require.NoError(t, pusher.PushToUID(context.Background(), "1", 1, wrapperspb.Bytes(nil)))
	require.Equal(t, uint64(1), received.Load())
	received.Store(0)
	measurements.collect(t)
	return pusher, received
}

type applicationServer interface {
	transport.Server
	transport.Endpointer
	BeforeStart(context.Context) error
}

func startServer(t testing.TB, server applicationServer, service, id string, metadata map[string]string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	appCtx := kratos.NewContext(ctx, kratos.New(kratos.ID(id), kratos.Name(service), kratos.Metadata(metadata)))
	require.NoError(t, server.BeforeStart(appCtx))
	done := make(chan error, 1)
	go func() { done <- server.Start(appCtx) }()
	t.Cleanup(func() {
		cancel()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		require.NoError(t, server.Stop(stopCtx))
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-stopCtx.Done():
			t.Error("benchmark server did not stop")
		}
	})
	endpoint, err := server.Endpoint()
	require.NoError(t, err)
	// 单 endpoint 就绪探针使用 pick_first，避免 Kratos 默认 selector 的初始化顺序约束。
	connection, err := kgrpc.NewClient(ctx,
		kgrpc.WithEndpoint("direct:///"+endpoint.Host), kgrpc.WithTimeout(0),
		kgrpc.WithOptions(grpc.WithDefaultServiceConfig(`{"loadBalancingConfig":[{"pick_first":{}}]}`)),
	)
	require.NoError(t, err)
	defer func() { require.NoError(t, connection.Close()) }()
	readyCtx, readyCancel := context.WithTimeout(ctx, 3*time.Second)
	defer readyCancel()
	for {
		state := connection.GetState()
		if state == connectivity.Ready {
			return
		}
		require.True(t, connection.WaitForStateChange(readyCtx, state), "benchmark gRPC server did not become ready")
	}
}

type benchmarkAuthenticator struct{}

func (benchmarkAuthenticator) Authenticate(_ context.Context, _ string, token []byte, _ string) (string, error) {
	return string(token), nil
}

// unusedDiscovery 确认本基准的 Push 回程不会查询服务发现。
type unusedDiscovery struct{}

func (unusedDiscovery) GetService(context.Context, string) ([]*registry.ServiceInstance, error) {
	return nil, errors.New("push benchmark unexpectedly queried service discovery")
}

func (unusedDiscovery) Watch(context.Context, string) (registry.Watcher, error) {
	return nil, errors.New("push benchmark unexpectedly watched service discovery")
}

// benchmarkConnection 保留真实 Gateway 的路由校验，以计数接收器替代客户端 socket。
// delay 只模拟 Gateway RPC 处理延迟，不能解释成真实慢客户端或 WebSocket 容量。
type benchmarkConnection struct {
	id       string
	received *atomic.Uint64
	delay    time.Duration
}

func (c *benchmarkConnection) ConnID() string                                { return c.id }
func (*benchmarkConnection) RemoteAddr() string                              { return "127.0.0.1:1" }
func (*benchmarkConnection) Close() error                                    { return nil }
func (*benchmarkConnection) CloseWithProto(context.Context, *v1.Proto) error { return nil }

func (c *benchmarkConnection) SendProto(message *v1.Proto) error {
	if message.Op != v1.OpPush {
		return nil
	}
	if c.delay > 0 {
		time.Sleep(c.delay)
	}
	c.received.Add(1)
	return nil
}
