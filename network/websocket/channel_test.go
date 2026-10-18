package websocket

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"yola/api/protocol/v1"
	"yola/network"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestChannelSendProto(t *testing.T) {
	ch := &Channel{
		codec:    defaultCodec(),
		outbound: make(chan outboundFrame, 1),
	}
	want := &v1.Proto{Op: v1.OpPush, Cmd: 1001, Body: []byte("push")}
	if err := ch.SendProto(want); err != nil {
		t.Fatal(err)
	}

	got := &v1.Proto{}
	if err := proto.Unmarshal((<-ch.outbound).body, got); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(want, got) {
		t.Fatalf("want %v, got %v", want, got)
	}
}

func TestChannelSendProtoRejectsFullQueue(t *testing.T) {
	codec := &countingCodec{}
	ch := &Channel{
		codec:    codec,
		outbound: make(chan outboundFrame, 1),
	}
	ch.outbound <- outboundFrame{}
	ping := &v1.Proto{Op: v1.OpHeartbeat}
	if err := ch.SendProto(ping); !errors.Is(err, network.ErrSendQueueFull) {
		t.Fatalf("SendProto() error = %v, want %v", err, network.ErrSendQueueFull)
	}
	if codec.marshals.Load() != 0 {
		t.Fatal("SendProto encoded a frame after the outbound queue was already full")
	}
}

func TestChannelSendProtoRejectsClosedChannel(t *testing.T) {
	codec := &countingCodec{}
	ch := &Channel{
		codec:    codec,
		outbound: make(chan outboundFrame, 1),
	}
	ch.closed.Store(true)
	if err := ch.SendProto(&v1.Proto{Op: v1.OpHeartbeat}); !errors.Is(err, network.ErrConnectionClosed) {
		t.Fatalf("SendProto() error = %v, want %v", err, network.ErrConnectionClosed)
	}
	if codec.marshals.Load() != 0 {
		t.Fatal("SendProto encoded a frame after the channel was already closed")
	}
}

func TestChannelSendPreparedSharesDefaultEncoding(t *testing.T) {
	prepared := &network.PreparedProto{}
	prepared.Reset(&v1.Proto{Op: v1.OpPush, Cmd: 1001, Body: []byte("shared")})
	first := &Channel{codec: defaultCodec(), outbound: make(chan outboundFrame, 1)}
	second := &Channel{codec: defaultCodec(), outbound: make(chan outboundFrame, 1)}
	if err := first.SendPrepared(prepared); err != nil {
		t.Fatal(err)
	}
	if err := second.SendPrepared(prepared); err != nil {
		t.Fatal(err)
	}
	firstBody := (<-first.outbound).body
	secondBody := (<-second.outbound).body
	if len(firstBody) == 0 || &firstBody[0] != &secondBody[0] {
		t.Fatal("prepared WebSocket frames did not share immutable encoding")
	}
}

func TestChannelSendPreparedPreservesCustomCodec(t *testing.T) {
	codec := &countingCodec{}
	prepared := &network.PreparedProto{}
	prepared.Reset(&v1.Proto{Op: v1.OpPush})
	first := &Channel{codec: codec, outbound: make(chan outboundFrame, 1)}
	second := &Channel{codec: codec, outbound: make(chan outboundFrame, 1)}
	if err := first.SendPrepared(prepared); err != nil {
		t.Fatal(err)
	}
	if err := second.SendPrepared(prepared); err != nil {
		t.Fatal(err)
	}
	if codec.marshals.Load() != 2 {
		t.Fatalf("custom codec marshal calls = %d, want 2", codec.marshals.Load())
	}
}

func TestChannelRejectsOversizedProtoBeforeQueue(t *testing.T) {
	tests := []struct {
		name string
		op   int32
		send func(*Channel, *v1.Proto) error
	}{
		{name: "push", op: v1.OpPush, send: func(ch *Channel, p *v1.Proto) error { return ch.SendProto(p) }},
		{
			name: "final",
			op:   v1.OpKick,
			send: func(ch *Channel, p *v1.Proto) error {
				return ch.CloseWithProto(context.Background(), p)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch := &Channel{
				codec:      fixedSizeCodec{size: v1.MaxProtoSize + 1},
				outbound:   make(chan outboundFrame, 1),
				writerDone: make(chan struct{}),
			}
			message := &v1.Proto{Op: tt.op, Body: []byte("small")}
			if err := tt.send(ch, message); !errors.Is(err, network.ErrFrameTooLarge) {
				t.Fatalf("send() error = %v, want %v", err, network.ErrFrameTooLarge)
			}
			if len(ch.outbound) != 0 {
				t.Fatal("oversized frame entered outbound queue")
			}
			if ch.closed.Load() {
				t.Fatal("rejected final frame closed the channel")
			}
		})
	}
}

func TestChannelUsesSerializedSizeInsteadOfBodySize(t *testing.T) {
	ch := &Channel{
		codec:    fixedSizeCodec{size: 1},
		outbound: make(chan outboundFrame, 1),
	}
	message := &v1.Proto{Op: v1.OpPush, Body: make([]byte, v1.MaxProtoSize+1)}
	if err := ch.SendProto(message); err != nil {
		t.Fatalf("SendProto() error = %v, want nil", err)
	}
	if len(ch.outbound) != 1 {
		t.Fatal("valid serialized frame was not queued")
	}
}

func TestCloseWithProtoReturnsWhenEarlierWriteFails(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	controlled := &controlledWriteConn{
		Conn:    clientConn,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	wsConn := openTestWebSocket(t, controlled, serverConn)
	ch := newChannel(context.Background(), wsConn, defaultCodec(), ChannelConfig{
		WriteTimeout:  20 * time.Millisecond,
		ReadDeadline:  time.Second,
		SendQueueSize: 1,
	})
	controlled.fail.Store(true)
	if err := ch.SendProto(&v1.Proto{Op: v1.OpPush}); err != nil {
		t.Fatal(err)
	}
	waitWebSocketValue(t, controlled.started)

	closed := make(chan error, 1)
	go func() {
		closed <- ch.CloseWithProto(context.Background(), &v1.Proto{Op: v1.OpKick})
	}()
	deadline := time.Now().Add(time.Second)
	for len(ch.outbound) != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(ch.outbound) != 1 {
		t.Fatal("final frame was not queued behind the blocked write")
	}
	close(controlled.release)
	if err := waitWebSocketValue(t, closed); !errors.Is(err, errControlledWrite) {
		t.Fatalf("CloseWithProto() error = %v, want %v", err, errControlledWrite)
	}
	waitWebSocketValue(t, ch.writerDone)
}

func TestCloseWithProtoReturnsWhenWriterExitsBeforeFinalEnqueue(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	controlled := &controlledWriteConn{
		Conn:    clientConn,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	wsConn := openTestWebSocket(t, controlled, serverConn)
	channelCtx, cancelChannel := context.WithCancel(context.Background())
	t.Cleanup(cancelChannel)
	release := sync.OnceFunc(func() { close(controlled.release) })
	t.Cleanup(release)
	ch := newChannel(channelCtx, wsConn, defaultCodec(), ChannelConfig{
		WriteTimeout:  20 * time.Millisecond,
		ReadDeadline:  time.Second,
		SendQueueSize: 1,
	})
	controlled.fail.Store(true)
	if err := ch.SendProto(&v1.Proto{Op: v1.OpPush}); err != nil {
		t.Fatal(err)
	}
	waitWebSocketValue(t, controlled.started)
	if err := ch.SendProto(&v1.Proto{Op: v1.OpPush}); err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() {
		closed <- ch.CloseWithProto(context.Background(), &v1.Proto{Op: v1.OpKick})
	}()
	deadline := time.Now().Add(time.Second)
	for !ch.closed.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !ch.closed.Load() {
		t.Fatal("CloseWithProto did not start closing the channel")
	}
	cancelChannel()
	release()
	if err := waitWebSocketValue(t, closed); !errors.Is(err, errControlledWrite) {
		t.Fatalf("CloseWithProto() error = %v, want %v", err, errControlledWrite)
	}
	waitWebSocketValue(t, ch.writerDone)
}

func TestFormatCloseFrameLimitsReason(t *testing.T) {
	frame := formatCloseFrame(strings.Repeat("😀", 100))
	if len(frame) > 125 {
		t.Fatalf("close frame length = %d, want <= 125", len(frame))
	}
	if !utf8.Valid(frame[2:]) {
		t.Fatal("close reason is not valid UTF-8")
	}
}

func TestChannelSendConcurrentClose(t *testing.T) {
	endpoint := startWebSocketTestServer(t, websocketTestHandler{})
	client, err := NewClient(context.Background(), WithEndpoint(endpoint), WithServiceName("game"), WithToken("synthetic-token"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	ch := client.Channel()
	start := make(chan struct{})
	errs := make(chan error, 32)
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- ch.SendProto(&v1.Proto{Op: v1.OpHeartbeat})
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		_ = ch.Close()
	}()
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil && !errors.Is(err, network.ErrConnectionClosed) && !errors.Is(err, network.ErrSendQueueFull) {
			t.Fatalf("SendProto() error = %v", err)
		}
	}
}

func TestSendStatsQueueAndClose(t *testing.T) {
	ch := &Channel{codec: defaultCodec(), outbound: make(chan outboundFrame, 2)}
	observer, ok := any(ch).(network.SendStatsProvider)
	require.True(t, ok, "WebSocket connection must expose send capacity stats")
	require.NoError(t, ch.SendProto(&v1.Proto{Op: v1.OpPush, Body: make([]byte, 32)}))
	prepared := &network.PreparedProto{}
	prepared.Reset(&v1.Proto{Op: v1.OpPush, Body: make([]byte, 1024)})
	require.NoError(t, ch.SendPrepared(prepared))
	require.ErrorIs(t, ch.SendPrepared(prepared), network.ErrSendQueueFull)
	stats := observer.SendStats()
	require.Equal(t, 2, stats.QueueDepth)
	require.Equal(t, 2, stats.QueueCapacity)
	require.Equal(t, int64(1056), stats.PendingPayloadBytes)
	require.Equal(t, uint64(1), stats.QueueDropped)
	ch.markClosed()
	require.ErrorIs(t, ch.SendPrepared(prepared), network.ErrConnectionClosed)
	stats = observer.SendStats()
	require.True(t, stats.Closed)
	require.Equal(t, uint64(1), stats.QueueDropped)
	require.Equal(t, int64(1056), stats.PendingPayloadBytes)
}

func TestSendStatsSlowWriterAndCanceledFinal(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	controlled := &controlledWriteConn{Conn: clientConn, started: make(chan struct{}), release: make(chan struct{})}
	wsConn := openTestWebSocket(t, controlled, serverConn)
	controlled.fail.Store(true)
	ch := newChannel(context.Background(), wsConn, defaultCodec(), ChannelConfig{
		WriteTimeout:  20 * time.Millisecond,
		ReadDeadline:  time.Second,
		SendQueueSize: 1,
	})
	release := sync.OnceFunc(func() { close(controlled.release) })
	t.Cleanup(release)
	require.NoError(t, ch.SendProto(&v1.Proto{Op: v1.OpPush, Body: make([]byte, 32)}))
	waitWebSocketValue(t, controlled.started)
	require.Zero(t, ch.SendStats().PendingPayloadBytes)
	require.NoError(t, ch.SendProto(&v1.Proto{Op: v1.OpPush, Body: make([]byte, 1024)}))
	require.ErrorIs(t, ch.SendProto(&v1.Proto{Op: v1.OpPush}), network.ErrSendQueueFull)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	closed := make(chan error, 1)
	go func() { closed <- ch.CloseWithProto(ctx, &v1.Proto{Op: v1.OpKick, Body: make([]byte, 16)}) }()
	require.Eventually(t, func() bool { return ch.SendStats().PendingPayloadBytes == 1040 }, time.Second, time.Millisecond)
	cancel()
	require.ErrorIs(t, waitWebSocketValue(t, closed), context.Canceled)
	release()
	waitWebSocketValue(t, ch.writerDone)
	stats := ch.SendStats()
	require.Equal(t, int64(1024), stats.PendingPayloadBytes)
	require.Equal(t, uint64(1), stats.QueueDropped)
	require.True(t, stats.Closed)
}

func TestSendStatsConcurrentReadSendAndClose(t *testing.T) {
	ch := &Channel{codec: defaultCodec(), outbound: make(chan outboundFrame, 8)}
	var workers sync.WaitGroup
	middle := make(chan struct{})
	var reached sync.Once
	workers.Go(func() {
		<-middle
		ch.markClosed()
	})
	for range 4 {
		workers.Go(func() {
			for i := range 200 {
				if i == 100 {
					reached.Do(func() { close(middle) })
				}
				err := ch.SendProto(&v1.Proto{Op: v1.OpPush, Body: []byte("stats")})
				if err != nil {
					assert.True(t, errors.Is(err, network.ErrSendQueueFull) || errors.Is(err, network.ErrConnectionClosed))
				}
				assert.GreaterOrEqual(t, ch.SendStats().PendingPayloadBytes, int64(0))
			}
		})
	}
	workers.Wait()
	ch.markClosed()
	require.Equal(t, int64(ch.SendStats().QueueDepth*5), ch.SendStats().PendingPayloadBytes)
}

func TestChannelConfigValidate(t *testing.T) {
	tests := []struct {
		name   string
		config ChannelConfig
		want   string
	}{
		{
			name:   "write timeout",
			config: ChannelConfig{ReadDeadline: time.Second, SendQueueSize: 1},
			want:   "websocket: channel write timeout must be positive",
		},
		{
			name:   "read deadline",
			config: ChannelConfig{WriteTimeout: time.Second, SendQueueSize: 1},
			want:   "websocket: channel read deadline must be positive",
		},
		{
			name:   "send queue size",
			config: ChannelConfig{WriteTimeout: time.Second, ReadDeadline: time.Second},
			want:   "websocket: channel send queue size must be positive",
		},
		{
			name:   "valid",
			config: ChannelConfig{WriteTimeout: time.Second, ReadDeadline: time.Second, SendQueueSize: 1},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.config.validate()
			if test.want == "" {
				if err != nil {
					t.Fatalf("validate() error = %v, want nil", err)
				}
				return
			}
			if err == nil || err.Error() != test.want {
				t.Fatalf("validate() error = %v, want %q", err, test.want)
			}
		})
	}
}
