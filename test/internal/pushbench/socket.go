package pushbench

import (
	"context"
	"encoding/binary"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yola/api/protocol/v1"
	"yola/gateway"
	"yola/locate"
	locateredis "yola/locate/redis"
	"yola/network/websocket"
	"yola/node"

	"github.com/go-kratos/kratos/v3/encoding"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

type socketPipeline struct {
	ctx       context.Context
	origin    time.Time
	endpoint  string
	service   string
	locator   locate.Locator
	pusher    Pusher
	receivers []*socketReceiver
}

type socketReceiver struct {
	client       *websocket.Client
	disconnected chan struct{}
	table        int
	origin       time.Time
	measurements *measurements
	warmups      atomic.Uint64
	mu           sync.Mutex
	received     uint64
	err          error
}

func newSocketPipeline(t testing.TB, address, service string, tableCount int, measurements *measurements) *socketPipeline {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	client := redis.NewClient(&redis.Options{Addr: address})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	store := locateredis.New(client)
	socket := websocket.NewServer(websocket.Address("127.0.0.1:0"),
		websocket.MaxConnLimit(int32(tableCount*4+1)), websocket.MaxConnPerIP(int32(tableCount*4+1)))
	gate, err := gateway.NewServer(
		gateway.Address("127.0.0.1:0"), gateway.Auth(benchmarkAuthenticator{}),
		gateway.Locator(store), gateway.Discovery(unusedDiscovery{}), gateway.Transport(socket),
	)
	require.NoError(t, err)
	startServer(t, gate, "gateway", service+"-gateway", nil)
	server, err := node.NewServer(node.Address("127.0.0.1:0"),
		node.Locator(&measuredLocator{Locator: store, measurements: measurements}), node.ClientMiddleware(measurements.rpc))
	require.NoError(t, err)
	startServer(t, server, service, service+"-node", server.Metadata())
	endpoint, err := socket.Endpoint()
	require.NoError(t, err)
	pipeline := &socketPipeline{
		ctx: ctx, origin: time.Now(), endpoint: endpoint.String(), service: service, locator: store,
		pusher: &measuredPusher{server: server, measurements: measurements},
	}
	t.Cleanup(func() {
		cancel()
		for _, receiver := range pipeline.receivers {
			receiver.client.Close()
		}
		for _, receiver := range pipeline.receivers {
			receiver.waitClosed(t)
		}
	})
	for index := range tableCount * 4 {
		receiver := &socketReceiver{table: index / 4, origin: pipeline.origin, measurements: measurements}
		pipeline.connect(t, index, receiver, 0)
		pipeline.receivers = append(pipeline.receivers, receiver)
	}
	return pipeline
}

func (p *socketPipeline) connect(t testing.TB, index int, receiver *socketReceiver, readDelay time.Duration) {
	t.Helper()
	receiver.disconnected = make(chan struct{})
	options := []websocket.ClientOption{
		websocket.WithEndpoint(p.endpoint), websocket.WithServiceName(p.service), websocket.WithToken(strconv.Itoa(index + 1)),
		websocket.WithPushHandler(map[int32]websocket.PushHandler{1: receiver.receive}),
		websocket.WithDisconnectFunc(func(*websocket.Channel) { close(receiver.disconnected) }),
	}
	if readDelay > 0 {
		options = append(options, websocket.WithCodec(delayedReadCodec{Codec: encoding.GetCodec("proto"), delay: readDelay}))
	}
	client, err := websocket.NewClient(p.ctx, options...)
	require.NoError(t, err)
	receiver.client = client
}

func (p *socketPipeline) reconnect(t testing.TB, index int, readDelay time.Duration) {
	t.Helper()
	uid := strconv.Itoa(index + 1)
	before, err := p.locator.LocateGate(p.ctx, p.service, uid)
	require.NoError(t, err)
	receiver := p.receivers[index]
	receiver.client.Close()
	receiver.waitClosed(t)
	p.connect(t, index, receiver, readDelay)
	after, err := p.locator.LocateGate(p.ctx, p.service, uid)
	require.NoError(t, err)
	require.NotEqual(t, before.Binding.BindingToken, after.Binding.BindingToken)
}

func (r *socketReceiver) waitClosed(t testing.TB) {
	t.Helper()
	select {
	case <-r.disconnected:
	case <-time.After(5 * time.Second):
		t.Error("WebSocket receiver did not stop")
	}
}

func cadencePayload(table int, sequence uint64, scheduled time.Duration) *wrapperspb.BytesValue {
	data := make([]byte, 256)
	binary.LittleEndian.PutUint64(data, uint64(table))
	binary.LittleEndian.PutUint64(data[8:], sequence)
	// 同进程回环测试传相对时间，接收侧恢复 time.Time 的 monotonic 时钟。
	binary.LittleEndian.PutUint64(data[16:], uint64(scheduled))
	return wrapperspb.Bytes(data)
}

func (r *socketReceiver) receive(body []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return
	}
	message := new(wrapperspb.BytesValue)
	if err := proto.Unmarshal(body, message); err != nil {
		r.err = err
		return
	}
	if len(message.Value) != 256 || binary.LittleEndian.Uint64(message.Value) != uint64(r.table) {
		r.err = fmt.Errorf("unexpected payload for table %d", r.table)
		return
	}
	sequence := binary.LittleEndian.Uint64(message.Value[8:])
	if sequence == 0 {
		r.warmups.Add(1)
		return
	}
	if sequence != r.received+1 {
		r.err = fmt.Errorf("table %d received sequence %d, want %d", r.table, sequence, r.received+1)
		return
	}
	r.received++
	scheduled := time.Duration(binary.LittleEndian.Uint64(message.Value[16:]))
	r.measurements.record("client_scheduled", r.origin.Add(scheduled), nil)
}

func (p *socketPipeline) waitReceived(t testing.TB, want uint64) {
	t.Helper()
	require.Eventually(t, func() bool {
		for _, receiver := range p.receivers {
			receiver.mu.Lock()
			received, err := receiver.received, receiver.err
			receiver.mu.Unlock()
			if err != nil {
				t.Error(err)
				return true
			}
			if received != want {
				return false
			}
		}
		return true
	}, 10*time.Second, time.Millisecond, "every player must receive every broadcast in order")
}

// delayedReadCodec 在客户端读循环中延迟下一帧读取，保留实际 socket 背压。
type delayedReadCodec struct {
	encoding.Codec
	delay time.Duration
}

func (c delayedReadCodec) Unmarshal(data []byte, value any) error {
	if err := c.Codec.Unmarshal(data, value); err != nil {
		return err
	}
	if message, ok := value.(*v1.Proto); ok && message.Op == v1.OpPush {
		time.Sleep(c.delay)
	}
	return nil
}
