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

func TestSendStatsQueueAndClose(t *testing.T) {
	ch := newChannel(2, defaultCodec())
	var conn network.Connection = tcpConnection{ch: ch}
	observer, ok := conn.(network.SendStatsProvider)
	require.True(t, ok, "TCP connection must expose send capacity stats")
	for _, size := range []int{32, 1024} {
		require.NoError(t, conn.SendProto(&v1.Proto{Op: v1.OpPush, Body: make([]byte, size)}))
	}
	require.ErrorIs(t, conn.SendProto(&v1.Proto{Op: v1.OpPush}), network.ErrSendQueueFull)
	stats := observer.SendStats()
	require.Equal(t, 2, stats.QueueDepth)
	require.Equal(t, 2, stats.QueueCapacity)
	require.Equal(t, int64(1056), stats.PendingPayloadBytes)
	require.Equal(t, uint64(1), stats.QueueDropped)
	_, ready := ch.next()
	require.True(t, ready)
	require.Equal(t, int64(1024), observer.SendStats().PendingPayloadBytes)
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
	go server.dispatchTCP(controlled, bufio.NewWriterSize(controlled, defaultIOBufferSize), ch)
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
	waitTCPValue(t, ch.writerDone)
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
	workers.Go(func() { <-middle; ch.close() })
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
