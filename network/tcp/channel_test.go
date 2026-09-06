package tcp

import (
	"bufio"
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"yola/api/protocol/v1"
	"yola/network"
)

func TestPushReportsQueueAndClosedState(t *testing.T) {
	ch := newChannel(1, defaultCodec())
	message := &v1.Proto{Op: v1.OpPush}
	if err := ch.push(message); err != nil {
		t.Fatal(err)
	}
	if err := ch.push(message); !errors.Is(err, network.ErrSendQueueFull) {
		t.Fatalf("push() error = %v, want %v", err, network.ErrSendQueueFull)
	}
	if event, ok := ch.next(); !ok || event.done != nil || event.proto != message {
		t.Fatalf("next() = %#v", event)
	}
	ch.close()
	ch.close()
	if err := ch.push(message); !errors.Is(err, network.ErrConnectionClosed) {
		t.Fatalf("push() error = %v, want %v", err, network.ErrConnectionClosed)
	}
}

func TestChannelRejectsOversizedProtoBeforeQueue(t *testing.T) {
	tests := []struct {
		name string
		op   int32
		send func(*channel, *v1.Proto) error
	}{
		{name: "push", op: v1.OpPush, send: func(ch *channel, p *v1.Proto) error {
			return tcpConnection{ch: ch}.SendProto(p)
		}},
		{name: "response", op: v1.OpResponse, send: func(ch *channel, p *v1.Proto) error {
			return ch.reply(context.Background(), p)
		}},
		{name: "final", op: v1.OpKick, send: func(ch *channel, p *v1.Proto) error {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			return ch.closeWithProto(ctx, p)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch := newChannel(1, fixedSizeCodec{size: v1.MaxProtoSize + 1})
			message := &v1.Proto{Op: tt.op, Body: []byte("small")}
			if err := tt.send(ch, message); !errors.Is(err, network.ErrFrameTooLarge) {
				t.Fatalf("send() error = %v, want %v", err, network.ErrFrameTooLarge)
			}
			if len(ch.outbound) != 0 {
				t.Fatal("oversized frame entered outbound queue")
			}
			if ch.closed {
				t.Fatal("rejected final frame closed the channel")
			}
		})
	}
}

func TestConnectionCloseRejectsOversizedProtoWithoutClosingSocket(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() { _ = serverConn.Close() })
	t.Cleanup(func() { _ = clientConn.Close() })
	ch := newChannel(1, fixedSizeCodec{size: v1.MaxProtoSize + 1})
	conn := tcpConnection{conn: serverConn, ch: ch}

	err := conn.CloseWithProto(context.Background(), &v1.Proto{Op: v1.OpKick})
	if !errors.Is(err, network.ErrFrameTooLarge) {
		t.Fatalf("CloseWithProto() error = %v, want %v", err, network.ErrFrameTooLarge)
	}
	if ch.closed {
		t.Fatal("oversized final frame closed the channel")
	}

	read := make(chan error, 1)
	go func() {
		var body [1]byte
		_, readErr := serverConn.Read(body[:])
		read <- readErr
	}()
	if _, err = clientConn.Write([]byte{1}); err != nil {
		t.Fatalf("socket write after rejected final frame: %v", err)
	}
	if err = <-read; err != nil {
		t.Fatalf("socket read after rejected final frame: %v", err)
	}
}

func TestChannelUsesSerializedSizeInsteadOfBodySize(t *testing.T) {
	ch := newChannel(1, fixedSizeCodec{size: 1})
	message := &v1.Proto{Op: v1.OpPush, Body: make([]byte, v1.MaxProtoSize+1)}
	if err := ch.push(message); err != nil {
		t.Fatalf("push() error = %v, want nil", err)
	}
	if len(ch.outbound) != 1 {
		t.Fatal("valid serialized frame was not queued")
	}
}

func TestReplyWaitsForQueueAndCloseCancelsIt(t *testing.T) {
	ch := newChannel(1, defaultCodec())
	if err := ch.push(&v1.Proto{Op: v1.OpPush}); err != nil {
		t.Fatal(err)
	}
	replied := make(chan error, 1)
	go func() {
		replied <- ch.reply(context.Background(), &v1.Proto{Op: v1.OpResponse})
	}()
	select {
	case err := <-replied:
		t.Fatalf("reply() returned before queue capacity was available: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	ch.close()
	select {
	case err := <-replied:
		if !errors.Is(err, network.ErrConnectionClosed) {
			t.Fatalf("reply() error = %v, want %v", err, network.ErrConnectionClosed)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("reply remained blocked after close")
	}
}

func TestReplyWaitsForFinalClose(t *testing.T) {
	ch := newChannel(1, defaultCodec())
	if err := ch.push(&v1.Proto{Op: v1.OpPush}); err != nil {
		t.Fatal(err)
	}
	replied := make(chan error, 1)
	go func() {
		replied <- ch.reply(context.Background(), &v1.Proto{Op: v1.OpResponse})
	}()
	select {
	case err := <-replied:
		t.Fatalf("reply() returned before close started: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	closed := make(chan error, 1)
	go func() {
		closed <- ch.closeWithProto(context.Background(), &v1.Proto{Op: v1.OpKick})
	}()
	<-ch.closing
	select {
	case err := <-replied:
		t.Fatalf("reply() returned before final frame completed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	<-ch.outbound
	final := <-ch.outbound
	if final.done == nil {
		t.Fatal("close did not enqueue a final frame")
	}
	final.done <- nil
	ch.close()
	if err := <-closed; err != nil {
		t.Fatalf("closeWithProto() error = %v", err)
	}
	if err := <-replied; !errors.Is(err, network.ErrConnectionClosed) {
		t.Fatalf("reply() error = %v, want %v", err, network.ErrConnectionClosed)
	}
}

func TestReplyHonorsCanceledContext(t *testing.T) {
	ch := newChannel(1, defaultCodec())
	if err := ch.push(&v1.Proto{Op: v1.OpPush}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ch.reply(ctx, &v1.Proto{Op: v1.OpResponse}); !errors.Is(err, context.Canceled) {
		t.Fatalf("reply() error = %v, want %v", err, context.Canceled)
	}
}

func TestReplyRejectsClosedChannelWithoutEnqueue(t *testing.T) {
	ch := newChannel(1, defaultCodec())
	ch.close()
	if err := ch.reply(context.Background(), &v1.Proto{Op: v1.OpResponse}); !errors.Is(err, network.ErrConnectionClosed) {
		t.Fatalf("reply() error = %v, want %v", err, network.ErrConnectionClosed)
	}
	if len(ch.outbound) != 0 {
		t.Fatal("reply enqueued a response after close")
	}
}

func TestWaitFinalPrefersCompletedWrite(t *testing.T) {
	ch := newChannel(1, defaultCodec())
	result := make(chan error, 1)
	result <- nil
	ch.close()
	if err := ch.waitFinal(context.Background(), result); err != nil {
		t.Fatalf("waitFinal() error = %v, want nil", err)
	}
}

func TestCloseWithProtoReturnsWhenStoppedBeforeWriterStarts(t *testing.T) {
	ch := newChannel(1, defaultCodec())
	closed := make(chan error, 1)
	go func() {
		closed <- ch.closeWithProto(context.Background(), &v1.Proto{Op: v1.OpKick})
	}()
	select {
	case event := <-ch.outbound:
		if event.done == nil {
			t.Fatal("close did not enqueue a final frame")
		}
	case <-time.After(time.Second):
		t.Fatal("close did not enqueue the final frame")
	}
	ch.close()
	select {
	case err := <-closed:
		if !errors.Is(err, network.ErrConnectionClosed) {
			t.Fatalf("closeWithProto() error = %v, want %v", err, network.ErrConnectionClosed)
		}
	case <-time.After(time.Second):
		t.Fatal("closeWithProto remained blocked without a writer")
	}
}

func TestDispatchWriteDeadlineClosesSlowConsumer(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() { _ = clientConn.Close() })
	ch := newChannel(1, defaultCodec())
	server := NewServer(WriteTimeout(20 * time.Millisecond))
	dispatched := make(chan struct{})
	startedAt := time.Now()
	go func() {
		server.dispatchTCP(serverConn, bufio.NewWriterSize(serverConn, defaultIOBufferSize), ch)
		close(dispatched)
	}()
	if err := ch.push(&v1.Proto{Op: v1.OpPush, Body: make([]byte, v1.MaxProtoSize-64)}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-dispatched:
	case <-time.After(time.Second):
		t.Fatal("writer remained blocked after write deadline")
	}
	select {
	case <-ch.writerDone:
	default:
		t.Fatal("writer exit did not close channel")
	}
	var timeout net.Error
	if err := ch.writerError(); !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Fatalf("writer error = %v, want write timeout", err)
	}
	if elapsed := time.Since(startedAt); elapsed < 10*time.Millisecond {
		t.Fatalf("writer exited in %v, want write deadline path", elapsed)
	}
}

func TestCloseWithProtoReturnsWhenWriterFailsWithFullQueue(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() { _ = clientConn.Close() })
	writeStarted := make(chan struct{})
	conn := &writeStartedConn{Conn: serverConn, started: writeStarted}
	ch := newChannel(1, defaultCodec())
	server := NewServer(WriteTimeout(20 * time.Millisecond))
	dispatched := make(chan struct{})
	go func() {
		server.dispatchTCP(conn, bufio.NewWriterSize(conn, defaultIOBufferSize), ch)
		close(dispatched)
	}()
	if err := ch.push(&v1.Proto{Op: v1.OpPush, Body: make([]byte, v1.MaxProtoSize-64)}); err != nil {
		t.Fatal(err)
	}
	waitTCPValue(t, writeStarted)
	if err := ch.push(&v1.Proto{Op: v1.OpPush}); err != nil {
		t.Fatal(err)
	}

	closed := make(chan error, 1)
	go func() {
		closed <- ch.closeWithProto(context.Background(), &v1.Proto{Op: v1.OpKick})
	}()
	err := waitTCPValue(t, closed)
	var timeout net.Error
	if !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Fatalf("closeWithProto() error = %v, want write timeout", err)
	}
	waitTCPValue(t, dispatched)
}

type writeStartedConn struct {
	net.Conn
	started chan struct{}
	once    sync.Once
}

func (c *writeStartedConn) Write(body []byte) (int, error) {
	c.once.Do(func() { close(c.started) })
	return c.Conn.Write(body)
}

func TestConnectionSendConcurrentClose(t *testing.T) {
	for range 1000 {
		clientConn, serverConn := net.Pipe()
		ch := newChannel(1, defaultCodec())
		conn := tcpConnection{conn: clientConn, ch: ch}
		start := make(chan struct{})
		sendDone := make(chan error, 1)
		closeDone := make(chan error, 1)
		go func() {
			<-start
			sendDone <- conn.SendProto(&v1.Proto{Op: v1.OpPush})
		}()
		go func() {
			<-start
			closeDone <- conn.Close()
		}()
		close(start)
		sendErr := <-sendDone
		if sendErr != nil && !errors.Is(sendErr, network.ErrConnectionClosed) {
			t.Fatalf("SendProto() error = %v", sendErr)
		}
		if err := <-closeDone; err != nil {
			t.Fatalf("Close() error = %v", err)
		}
		if err := conn.SendProto(&v1.Proto{Op: v1.OpHeartbeat}); !errors.Is(err, network.ErrConnectionClosed) {
			t.Fatalf("SendProto() after Close error = %v, want %v", err, network.ErrConnectionClosed)
		}
		_ = serverConn.Close()
	}
}
