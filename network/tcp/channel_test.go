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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChannelRejectsOversizedProtoBeforeQueue(t *testing.T) {
	tests := []struct {
		name string
		op   int32
		send func(*channel, *v1.Proto) error
	}{
		{
			name: "push",
			op:   v1.OpPush,
			send: func(ch *channel, p *v1.Proto) error {
				return tcpConnection{ch: ch}.SendProto(p)
			},
		},
		{
			name: "response",
			op:   v1.OpResponse,
			send: func(ch *channel, p *v1.Proto) error {
				return ch.reply(context.Background(), p)
			},
		},
		{
			name: "final",
			op:   v1.OpKick,
			send: func(ch *channel, p *v1.Proto) error {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
				defer cancel()
				return ch.closeWithProto(ctx, p)
			},
		},
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
	t.Cleanup(func() {
		ch.close()
		_ = waitTCPValue(t, replied)
	})
	go func() {
		defer close(replied)
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
	t.Cleanup(func() {
		ch.close()
		_ = waitTCPValue(t, replied)
	})
	go func() {
		defer close(replied)
		replied <- ch.reply(context.Background(), &v1.Proto{Op: v1.OpResponse})
	}()
	select {
	case err := <-replied:
		t.Fatalf("reply() returned before close started: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	closed := make(chan error, 1)
	t.Cleanup(func() {
		ch.close()
		_ = waitTCPValue(t, closed)
	})
	go func() {
		defer close(closed)
		closed <- ch.closeWithProto(context.Background(), &v1.Proto{Op: v1.OpKick})
	}()
	waitTCPValue(t, ch.closing)
	select {
	case err := <-replied:
		t.Fatalf("reply() returned before final frame completed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	waitTCPValue(t, ch.outbound)
	final := waitTCPValue(t, ch.outbound)
	if final.done == nil {
		t.Fatal("close did not enqueue a final frame")
	}
	final.done <- nil
	ch.close()
	if err := waitTCPValue(t, closed); err != nil {
		t.Fatalf("closeWithProto() error = %v", err)
	}
	if err := waitTCPValue(t, replied); !errors.Is(err, network.ErrConnectionClosed) {
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
	t.Cleanup(func() {
		ch.close()
		_ = waitTCPValue(t, closed)
	})
	go func() {
		defer close(closed)
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
		server.dispatchTCP(serverConn, bufio.NewWriterSize(serverConn, _defaultIOBufferSize), ch)
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
	case <-ch.stop:
	default:
		t.Fatal("writer exit did not stop channel")
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
		server.dispatchTCP(conn, bufio.NewWriterSize(conn, _defaultIOBufferSize), ch)
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

func TestSendStatsQueueAndClose(t *testing.T) {
	ch := newChannel(2, defaultCodec())
	var conn network.Connection = tcpConnection{ch: ch}
	observer, ok := conn.(network.SendStatsProvider)
	require.True(t, ok, "TCP connection must expose send capacity stats")
	first := &v1.Proto{Op: v1.OpPush, Body: make([]byte, 32)}
	require.NoError(t, conn.SendProto(first))
	require.NoError(t, conn.SendProto(&v1.Proto{Op: v1.OpPush, Body: make([]byte, 1024)}))
	require.ErrorIs(t, conn.SendProto(&v1.Proto{Op: v1.OpPush}), network.ErrSendQueueFull)
	stats := observer.SendStats()
	require.Equal(t, 2, stats.QueueDepth)
	require.Equal(t, 2, stats.QueueCapacity)
	require.Equal(t, int64(1056), stats.PendingPayloadBytes)
	require.Equal(t, uint64(1), stats.QueueDropped)
	event, ready := ch.next()
	require.True(t, ready)
	require.Same(t, first, event.proto)
	require.Nil(t, event.done)
	require.Equal(t, int64(1024), observer.SendStats().PendingPayloadBytes)
	ch.close()
	ch.close()
	require.ErrorIs(t, conn.SendProto(&v1.Proto{Op: v1.OpPush}), network.ErrConnectionClosed)
	stats = observer.SendStats()
	require.True(t, stats.Closed)
	require.Equal(t, uint64(1), stats.QueueDropped)
	require.Equal(t, int64(1024), stats.PendingPayloadBytes)
}

func TestSendStatsSlowWriterAndCanceledReply(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() { _ = clientConn.Close() })
	controlled := &writeStartedConn{Conn: serverConn, started: make(chan struct{})}
	ch := newChannel(1, defaultCodec())
	conn := tcpConnection{conn: controlled, ch: ch}
	server := NewServer(WriteTimeout(time.Second))
	writerDone := make(chan struct{})
	t.Cleanup(func() {
		_ = conn.Close()
		waitTCPValue(t, writerDone)
	})
	go func() {
		defer close(writerDone)
		server.dispatchTCP(controlled, bufio.NewWriterSize(controlled, _defaultIOBufferSize), ch)
	}()
	require.NoError(t, conn.SendProto(&v1.Proto{Op: v1.OpPush, Body: make([]byte, 32)}))
	waitTCPValue(t, controlled.started)
	require.Zero(t, conn.SendStats().PendingPayloadBytes, "writer owns the dequeued frame")
	require.NoError(t, conn.SendProto(&v1.Proto{Op: v1.OpPush, Body: make([]byte, 1024)}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	replied := make(chan error, 1)
	go func() { replied <- ch.reply(ctx, &v1.Proto{Op: v1.OpResponse, Body: make([]byte, 16)}) }()
	require.Eventually(t, func() bool { return conn.SendStats().PendingPayloadBytes == 1040 }, time.Second, time.Millisecond)
	cancel()
	require.ErrorIs(t, waitTCPValue(t, replied), context.Canceled)
	require.Equal(t, int64(1024), conn.SendStats().PendingPayloadBytes)
	require.ErrorIs(t, conn.SendProto(&v1.Proto{Op: v1.OpPush}), network.ErrSendQueueFull)
	require.NoError(t, conn.Close())
	waitTCPValue(t, writerDone)
	require.Equal(t, int64(1024), conn.SendStats().PendingPayloadBytes)
	require.Equal(t, uint64(1), conn.SendStats().QueueDropped)
}

func TestSendStatsConcurrentReadSendAndClose(t *testing.T) {
	ch := newChannel(8, defaultCodec())
	conn := tcpConnection{ch: ch}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, ok := ch.next(); !ok {
				return
			}
		}
	}()
	var workers sync.WaitGroup
	middle := make(chan struct{})
	var reached sync.Once
	workers.Go(func() {
		<-middle
		ch.close()
	})
	for range 4 {
		workers.Go(func() {
			for i := range 200 {
				if i == 100 {
					reached.Do(func() { close(middle) })
				}
				err := conn.SendProto(&v1.Proto{Op: v1.OpPush, Body: []byte("stats")})
				if err != nil {
					assert.True(t, errors.Is(err, network.ErrSendQueueFull) || errors.Is(err, network.ErrConnectionClosed))
				}
				assert.GreaterOrEqual(t, conn.SendStats().PendingPayloadBytes, int64(0))
			}
		})
	}
	workers.Wait()
	ch.close()
	waitTCPValue(t, done)
	require.Equal(t, int64(conn.SendStats().QueueDepth*5), conn.SendStats().PendingPayloadBytes)
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
