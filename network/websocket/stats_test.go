package websocket

import (
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
	ch := &Channel{ctx: context.Background(), codec: defaultCodec(), outbound: make(chan outboundFrame, 2)}
	observer, ok := any(ch).(network.SendStatsProvider)
	require.True(t, ok, "WebSocket connection must expose send capacity stats")
	require.NoError(t, ch.SendProto(&v1.Proto{Op: v1.OpPush, Body: make([]byte, 32)}))
	prepared := new(network.PreparedProto)
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
		WriteTimeout: 20 * time.Millisecond, ReadDeadline: time.Second, SendQueueSize: 1,
	})
	var once sync.Once
	release := func() { once.Do(func() { close(controlled.release) }) }
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
	ch := &Channel{ctx: context.Background(), codec: defaultCodec(), outbound: make(chan outboundFrame, 8)}
	var workers sync.WaitGroup
	middle := make(chan struct{})
	var reached sync.Once
	workers.Go(func() { <-middle; ch.markClosed() })
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
