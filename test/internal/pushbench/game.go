package pushbench

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yola/api/cluster/v1"
	protocolv1 "yola/api/protocol/v1"
	"yola/gateway"
	locateredis "yola/locate/redis"
	"yola/network/websocket"
	"yola/node"

	"github.com/go-kratos/kratos/contrib/registry/etcd/v3"
	"github.com/go-kratos/kratos/v3/encoding"
	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/registry"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	clientv3 "go.etcd.io/etcd/client/v3"
)

// GameProbe 记录完整游戏链路的延迟及逐 UID 消息流，不改变业务消息内容。
type GameProbe struct {
	Endpoint     string
	server       *node.Server
	measurements *measurements
	mu           sync.Mutex
	streams      map[string]*messageStream
	recording    atomic.Bool
	err          error
	requests     atomic.Int64
}

type messageStream struct {
	mu            sync.Mutex
	sent          hash.Hash
	received      hash.Hash
	sentCount     uint64
	receivedCount uint64
	pending       []time.Time
}

// GameRedis 仅连接显式提供的专用实例；完整对局还要求独立 etcd namespace。
func GameRedis(t *testing.T) *redis.Client {
	t.Helper()
	address := os.Getenv("YOLA_REDIS_INTEGRATION")
	if address == "" || os.Getenv("YOLA_ETCD_INTEGRATION") == "" {
		t.Skip("set YOLA_REDIS_INTEGRATION and YOLA_ETCD_INTEGRATION to disposable instances")
	}
	client := redis.NewClient(&redis.Options{Addr: address})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	require.NoError(t, client.Ping(t.Context()).Err())
	return client
}

// StartGame 装配真实 Node、Gateway、Redis Locator 和 etcd Registry，调用者提供游戏注册与排空。
func StartGame(
	t *testing.T, service string, client redis.UniversalClient, register func(*node.Server), drain node.DrainFunc,
	playerCount int, rpcTimeout time.Duration,
) *GameProbe {

	t.Helper()
	measurements := newMeasurements(t)
	probe := &GameProbe{measurements: measurements, streams: make(map[string]*messageStream)}
	registryClient, err := clientv3.New(clientv3.Config{
		Endpoints: []string{os.Getenv("YOLA_ETCD_INTEGRATION")}, DialTimeout: 3 * time.Second,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, registryClient.Close()) })
	suffix := rand.Text()
	discovery := etcd.New(registryClient, etcd.Namespace("/yola/game-test/"+suffix))
	store := locateredis.New(client)
	server, err := node.NewServer(
		node.Address("127.0.0.1:0"), node.Locator(&measuredLocator{Locator: store, measurements: measurements}),
		node.HandlerTimeout(rpcTimeout),
		node.ClientMiddleware(probe.observePush, measurements.rpc), node.Middleware(probe.observeHandler), node.Drain(drain),
	)
	require.NoError(t, err)
	register(server)
	probe.server = server
	startServer(t, server, service, service+"-"+suffix, server.Metadata())
	endpoint, err := server.Endpoint()
	require.NoError(t, err)
	instance := &registry.ServiceInstance{
		ID: service + "-" + suffix, Name: service, Metadata: server.Metadata(), Endpoints: []string{endpoint.String()},
	}
	require.NoError(t, discovery.Register(t.Context(), instance))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, discovery.Deregister(ctx, instance))
	})
	socket := websocket.NewServer(websocket.Address("127.0.0.1:0"), websocket.Timeout(rpcTimeout),
		websocket.MaxConnLimit(int32(playerCount+1)), websocket.MaxConnPerIP(int32(playerCount+1)))
	gate, err := gateway.NewServer(
		gateway.Address("127.0.0.1:0"), gateway.Auth(benchmarkAuthenticator{}), gateway.Locator(store),
		gateway.Discovery(discovery), gateway.Transport(socket), gateway.RPCTimeout(rpcTimeout),
	)
	require.NoError(t, err)
	startServer(t, gate, "gateway", "gateway-"+suffix, nil)
	socketEndpoint, err := socket.Endpoint()
	require.NoError(t, err)
	probe.Endpoint = socketEndpoint.String()
	return probe
}

func (p *GameProbe) observePush(next middleware.Handler) middleware.Handler {
	return func(ctx context.Context, request any) (any, error) {
		if push, ok := request.(*v1.PushRequest); ok {
			stream := p.stream(push.GetRoute().GetUid())
			stream.mu.Lock()
			writeMessageDigest(stream.sent, push.Command, push.Body)
			stream.sentCount++
			stream.pending = append(stream.pending, time.Now())
			stream.mu.Unlock()
		}
		reply, err := next(ctx, request)
		p.recordError(err)
		return reply, err
	}
}

func (p *GameProbe) observeHandler(next middleware.Handler) middleware.Handler {
	return func(ctx context.Context, request any) (any, error) {
		started := time.Now()
		reply, err := next(ctx, request)
		if p.recording.Load() {
			p.measurements.record("handler", started, err)
		}
		return reply, err
	}
}

// RecordPush 在客户端按 callback FIFO 记录原始业务 payload，然后才执行客户端行为。
func (p *GameProbe) RecordPush(uid int64, command int32, body []byte) {
	stream := p.stream(strconv.FormatInt(uid, 10))
	stream.mu.Lock()
	defer stream.mu.Unlock()
	writeMessageDigest(stream.received, command, body)
	stream.receivedCount++
	if len(stream.pending) > 0 {
		if p.recording.Load() {
			p.measurements.record("client_push", stream.pending[0], nil)
		}
		stream.pending[0] = time.Time{}
		stream.pending = stream.pending[1:]
	}
}

// ResetMeasurements 在客户端就绪后排除预热延迟；消息流校验始终覆盖完整生命周期。
func (p *GameProbe) ResetMeasurements(t *testing.T) {
	p.measurements.collect(t)
	p.recording.Store(true)
}

// ReportMeasurements 结束稳态计时；后续 Scene 校验和排空仍检查消息流与响应错误。
func (p *GameProbe) ReportMeasurements(t *testing.T) {
	p.recording.Store(false)
	p.measurements.report(t)
}

// Stop 等待游戏排空；客户端须继续接收直到 AssertDelivered 完成。
func (p *GameProbe) Stop(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool { return p.requests.Load() == 0 }, 10*time.Second, time.Millisecond, "client requests did not finish")
	p.recording.Store(false)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	require.NoError(t, p.server.Stop(ctx))
}

// AssertDelivered 验证每个 UID 的消息数量和有边界的 SHA-256 流摘要一致。
func (p *GameProbe) AssertDelivered(t *testing.T, playerCount int) {
	t.Helper()
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		require.NoError(collect, p.deliveryError(playerCount))
	}, 10*time.Second, 10*time.Millisecond)
	p.mu.Lock()
	defer p.mu.Unlock()
	var messages uint64
	for _, stream := range p.streams {
		stream.mu.Lock()
		messages += stream.sentCount
		stream.mu.Unlock()
	}
	t.Logf("GAME delivery: players=%d pushes=%d matched=true", len(p.streams), messages)
}

func (p *GameProbe) deliveryError(playerCount int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	if pending := p.requests.Load(); pending != 0 {
		return fmt.Errorf("game requests still pending: %d", pending)
	}
	if len(p.streams) != playerCount {
		return fmt.Errorf("message streams: players=%d want=%d", len(p.streams), playerCount)
	}
	for uid, stream := range p.streams {
		stream.mu.Lock()
		sent, received := stream.sentCount, stream.receivedCount
		matches := sent > 0 && sent == received && bytes.Equal(stream.sent.Sum(nil), stream.received.Sum(nil))
		stream.mu.Unlock()
		if !matches {
			return fmt.Errorf("message stream differs: uid=%s sent=%d received=%d", uid, sent, received)
		}
	}
	return nil
}

func (p *GameProbe) recordError(err error) {
	if err == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err == nil {
		p.err = err
	}
}

// Err 返回首次观测到的传输或业务响应错误。
func (p *GameProbe) Err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

// Codec 在真实帧编解码处观测请求往返，保留客户端原有的同步或异步调用方式。
// 请求计时从出站帧编码开始，包含出站队列等待，但不包含业务 payload 首次序列化。
func (p *GameProbe) Codec(onResponse func(int32, []byte) error) encoding.Codec {
	return &gameCodec{Codec: encoding.GetCodec("proto"), probe: p, onResponse: onResponse, pending: make(map[int32]time.Time)}
}

type gameCodec struct {
	encoding.Codec
	probe      *GameProbe
	onResponse func(int32, []byte) error
	mu         sync.Mutex
	pending    map[int32]time.Time
}

func (c *gameCodec) Marshal(value any) ([]byte, error) {
	if frame, ok := value.(*protocolv1.Proto); ok && frame.Op == protocolv1.OpRequest {
		c.mu.Lock()
		c.pending[frame.Seq] = time.Now()
		c.probe.requests.Add(1)
		c.mu.Unlock()
	}
	return c.Codec.Marshal(value)
}

func (c *gameCodec) Unmarshal(body []byte, value any) error {
	if err := c.Codec.Unmarshal(body, value); err != nil {
		return err
	}
	frame, ok := value.(*protocolv1.Proto)
	if !ok || frame.Op != protocolv1.OpResponse {
		return nil
	}
	c.mu.Lock()
	started, found := c.pending[frame.Seq]
	delete(c.pending, frame.Seq)
	c.mu.Unlock()
	var err error
	if !found {
		err = fmt.Errorf("response has no pending request: seq=%d command=%d", frame.Seq, frame.Cmd)
	} else if frame.Code != 0 {
		err = fmt.Errorf("gateway response: command=%d code=%d", frame.Cmd, frame.Code)
	} else if c.onResponse != nil {
		err = c.onResponse(frame.Cmd, frame.Body)
	}
	c.probe.recordError(err)
	if found && c.probe.recording.Load() {
		c.probe.measurements.record("client_wire_request", started, err)
	}
	if found {
		c.probe.requests.Add(-1)
	}
	return nil
}

func (p *GameProbe) stream(uid string) *messageStream {
	p.mu.Lock()
	defer p.mu.Unlock()
	stream := p.streams[uid]
	if stream == nil {
		stream = &messageStream{sent: sha256.New(), received: sha256.New()}
		p.streams[uid] = stream
	}
	return stream
}

func writeMessageDigest(digest hash.Hash, command int32, body []byte) {
	var header [8]byte
	binary.LittleEndian.PutUint32(header[:4], uint32(command))
	binary.LittleEndian.PutUint32(header[4:], uint32(len(body)))
	// hash.Hash.Write 的契约保证不会返回错误。
	_, _ = digest.Write(header[:])
	_, _ = digest.Write(body)
}
