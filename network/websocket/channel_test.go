package websocket

import (
	"bytes"
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

	"google.golang.org/protobuf/proto"
)

func TestChannelSendProto(t *testing.T) {
	ch := &Channel{
		ctx:      context.Background(),
		codec:    defaultCodec(),
		outbound: make(chan outboundFrame, 1),
	}
	want := &v1.Proto{Op: v1.OpPush, Cmd: 1001, Body: []byte("push")}
	if err := ch.SendProto(want); err != nil {
		t.Fatal(err)
	}

	got := new(v1.Proto)
	if err := proto.Unmarshal((<-ch.outbound).body, got); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(want, got) {
		t.Fatalf("want %v, got %v", want, got)
	}
}

func TestChannelSendProtoRejectsFullQueue(t *testing.T) {
	codec := new(countingCodec)
	ch := &Channel{
		ctx:      context.Background(),
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
	codec := new(countingCodec)
	ch := &Channel{
		ctx:      context.Background(),
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
	prepared := new(network.PreparedProto)
	prepared.Reset(&v1.Proto{Op: v1.OpPush, Cmd: 1001, Body: []byte("shared")})
	first := &Channel{ctx: context.Background(), codec: defaultCodec(), outbound: make(chan outboundFrame, 1)}
	second := &Channel{ctx: context.Background(), codec: defaultCodec(), outbound: make(chan outboundFrame, 1)}
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
	codec := new(countingCodec)
	prepared := new(network.PreparedProto)
	prepared.Reset(&v1.Proto{Op: v1.OpPush})
	first := &Channel{ctx: context.Background(), codec: codec, outbound: make(chan outboundFrame, 1)}
	second := &Channel{ctx: context.Background(), codec: codec, outbound: make(chan outboundFrame, 1)}
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
		{name: "response", op: v1.OpResponse, send: func(ch *Channel, p *v1.Proto) error { return ch.SendProto(p) }},
		{name: "final", op: v1.OpKick, send: func(ch *Channel, p *v1.Proto) error {
			return ch.CloseWithProto(context.Background(), p)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch := &Channel{
				ctx:        context.Background(),
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
		ctx:      context.Background(),
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

func TestReadBoundedFrame(t *testing.T) {
	tests := []struct {
		name    string
		body    []byte
		wantErr error
	}{
		{name: "empty"},
		{name: "fragmented reads", body: bytes.Repeat([]byte{1}, 257)},
		{name: "boundary", body: bytes.Repeat([]byte{2}, v1.MaxProtoSize)},
		{name: "oversized", body: bytes.Repeat([]byte{3}, v1.MaxProtoSize+1), wantErr: network.ErrFrameTooLarge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := &limitedChunkReader{Reader: bytes.NewReader(test.body), size: 7}
			got, err := readBoundedFrame(reader, make([]byte, v1.MaxProtoSize))
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("readBoundedFrame() error = %v, want %v", err, test.wantErr)
			}
			if test.wantErr == nil && !bytes.Equal(got, test.body) {
				t.Fatalf("readBoundedFrame() length = %d, want %d", len(got), len(test.body))
			}
		})
	}
}

type limitedChunkReader struct {
	*bytes.Reader
	size int
}

func (r *limitedChunkReader) Read(body []byte) (int, error) {
	return r.Reader.Read(body[:min(len(body), r.size)])
}

func TestOnlyDefaultCodecOptimizesFrames(t *testing.T) {
	if !canOptimizeFrames(defaultCodec()) {
		t.Fatal("default protobuf codec did not enable bounded buffer reuse")
	}
	if canOptimizeFrames(new(countingCodec)) {
		t.Fatal("configured codec unexpectedly enabled bounded buffer reuse")
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
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(controlled.release) }) }
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

func TestMarshalFrameRejectsInvalidSize(t *testing.T) {
	codec := defaultCodec()
	if _, err := marshalFrame(codec, nil); !errors.Is(err, errNilPayload) {
		t.Fatalf("marshalFrame(nil) error = %v, want %v", err, errNilPayload)
	}
	if _, err := marshalFrame(codec, new(v1.Proto)); !errors.Is(err, errFrameLength) {
		t.Fatalf("marshalFrame(empty) error = %v, want %v", err, errFrameLength)
	}
	message := &v1.Proto{Op: v1.OpPush, Body: make([]byte, v1.MaxProtoSize)}
	if _, err := marshalFrame(codec, message); !errors.Is(err, network.ErrFrameTooLarge) {
		t.Fatalf("marshalFrame(oversized) error = %v, want %v", err, network.ErrFrameTooLarge)
	}
	boundary := &v1.Proto{Op: v1.OpPush, Body: make([]byte, v1.MaxProtoSize-5)}
	body, err := marshalFrame(codec, boundary)
	if err != nil || len(body) != v1.MaxProtoSize {
		t.Fatalf("marshalFrame(boundary) length = %d, error = %v", len(body), err)
	}
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
