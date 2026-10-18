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
	"yola/push"

	"github.com/go-kratos/kratos/v3"
	"github.com/go-kratos/kratos/v3/registry"
	"github.com/go-kratos/kratos/v3/transport"
	kgrpc "github.com/go-kratos/kratos/v3/transport/grpc"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// Pusher 是桌广播基准所需的客户端投递能力。
type Pusher interface {
	PushToUID(context.Context, string, int32, proto.Message) error
}

func newPipeline(t testing.TB, address, service string, playerCount int, delay time.Duration, measurements *measurements) (Pusher, *atomic.Uint64) {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: address, ContextTimeoutEnabled: true})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	store := locateredis.New(client)
	gate, err := gateway.NewServer(
		gateway.Address("127.0.0.1:0"), gateway.Auth(benchmarkAuthenticator{}),
		gateway.Locator(store), gateway.Discovery(unusedDiscovery{}), gateway.LeaseTTL(time.Hour),
	)
	require.NoError(t, err)
	startServer(t, gate, "gateway", service+"-gateway", gatewayReady(gate))
	sender, err := push.NewSender(&measuredLocator{Locator: store, measurements: measurements}, push.ClientMiddleware(measurements.rpc))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sender.Close()) })
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
	pusher := &measuredPusher{sender: sender, service: service, measurements: measurements}
	// 在计时前完成连接、路由和 protobuf 首次调用，确认投递确实到达真实 Gateway。
	require.NoError(t, pusher.PushToUID(context.Background(), "1", 1, wrapperspb.Bytes(nil)))
	require.Equal(t, uint64(1), received.Load())
	received.Store(0)
	measurements.collect(t)
	return pusher, received
}

// startServer 使用原生 App，probe 通过公开行为确认 transport 真正可用。
func startServer(t testing.TB, server transport.Server, service, id string, probe func(context.Context) error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	app := kratos.New(
		kratos.Context(ctx), kratos.ID(id), kratos.Name(service), kratos.Server(server),
		kratos.StopTimeout(10*time.Second),
	)
	var runErr error
	go func() {
		runErr = app.Run()
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		stopCtx, stopCancel := context.WithTimeout(kratos.NewContext(context.Background(), app), 10*time.Second)
		defer stopCancel()
		cleanupErr := server.Stop(stopCtx)
		select {
		case <-done:
			cleanupErr = errors.Join(cleanupErr, runErr)
		case <-stopCtx.Done():
			cleanupErr = errors.Join(cleanupErr, errors.New("benchmark application did not stop"), stopCtx.Err())
		}
		require.NoError(t, cleanupErr)
	})
	readyCtx, readyCancel := context.WithTimeout(ctx, 10*time.Second)
	defer readyCancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			require.NoError(t, runErr)
			t.Fatal("benchmark application exited before startup completed")
		case <-readyCtx.Done():
			t.Fatal("benchmark application did not become ready")
		case <-ticker.C:
			if probe(readyCtx) == nil {
				return
			}
		}
	}
}

func gatewayReady(gate *gateway.Server) func(context.Context) error {
	return func(ctx context.Context) error {
		connection := &benchmarkConnection{id: "readiness-probe"}
		if err := gate.Open(ctx, connection); err != nil {
			return err
		}
		gate.Close(ctx, connection)
		return nil
	}
}

func nodeReady(t testing.TB, address string) func(context.Context) error {
	t.Helper()
	connection, err := kgrpc.NewClient(context.Background(),
		kgrpc.WithEndpoint("direct:///"+address), kgrpc.WithTimeout(0),
		kgrpc.WithOptions(grpc.WithDefaultServiceConfig(`{"loadBalancingConfig":[{"pick_first":{}}]}`)),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	client := grpc_health_v1.NewHealthClient(connection)
	return func(ctx context.Context) error {
		callCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		response, err := client.Check(callCtx, &grpc_health_v1.HealthCheckRequest{})
		if err != nil {
			return err
		}
		if response.Status != grpc_health_v1.HealthCheckResponse_SERVING {
			return errors.New("node is not serving")
		}
		return nil
	}
}

type measuredPusher struct {
	sender       *push.Sender
	service      string
	measurements *measurements
}

func (p *measuredPusher) PushToUID(ctx context.Context, uid string, command int32, message proto.Message) error {
	started := time.Now()
	err := p.sender.PushToUID(ctx, p.service, uid, command, message)
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
