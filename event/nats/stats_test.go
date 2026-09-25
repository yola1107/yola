package nats

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"yola/event"

	natsgo "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

func TestSubscriptionStatsBacklogAndClose(t *testing.T) {
	bus := newTestBus(t, startTestServer(t), WithQueueCapacity(2), WithMaxPayloadBytes(16))
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	sub, err := bus.Subscribe(context.Background(), "yola.stats.backlog", func(context.Context, event.Event) {
		close(started)
		<-release
	})
	require.NoError(t, err)
	observer, ok := sub.(event.SubscriptionStatsProvider)
	require.True(t, ok, "NATS subscription must expose local capacity stats")
	require.NoError(t, bus.conn.Publish("yola.stats.backlog", []byte("first")))
	waitSignal(t, started, "handler did not start")
	for range 5 {
		require.NoError(t, bus.conn.Publish("yola.stats.backlog", make([]byte, 32)))
	}
	require.NoError(t, bus.conn.Flush())
	require.Eventually(t, func() bool { return observer.SubscriptionStats().QueueDropped == 3 }, time.Second, time.Millisecond)
	stats := observer.SubscriptionStats()
	require.Equal(t, 2, stats.QueueDepth)
	require.Equal(t, 2, stats.QueueCapacity)
	require.True(t, stats.QueueDroppedCurrent)
	require.True(t, stats.HandlerActive)
	require.Zero(t, stats.PayloadDropped, "oversized messages have not left the queue")
	// 留出计时器分辨率；拥塞交错仍由 started/release 屏障控制。
	<-time.After(5 * time.Millisecond)

	readDone := make(chan struct{})
	var readers sync.WaitGroup
	for range 4 {
		readers.Go(func() {
			for {
				select {
				case <-readDone:
					return
				default:
					observer.SubscriptionStats()
				}
			}
		})
	}
	sub.(*subscription).beginStop()
	unblock()
	require.NoError(t, sub.Unsubscribe(context.Background()))
	close(readDone)
	readers.Wait()
	stats = observer.SubscriptionStats()
	require.True(t, stats.Closed)
	require.False(t, stats.HandlerActive)
	require.False(t, stats.QueueDroppedCurrent)
	require.Equal(t, uint64(3), stats.QueueDropped)
	require.Equal(t, uint64(1), stats.HandlerCalls)
	require.Positive(t, stats.HandlerDuration)
	require.Equal(t, stats.HandlerDuration, stats.LastHandlerDuration)
	require.Equal(t, stats.HandlerDuration, stats.MaxHandlerDuration)
	require.Zero(t, stats.QueueDepth)
	require.Equal(t, stats, observer.SubscriptionStats())
}

func TestSubscriptionStatsNativeClosed(t *testing.T) {
	bus, err := New(WithURL(startTestServer(t)), WithQueueCapacity(1))
	require.NoError(t, err)
	t.Cleanup(func() {
		closeErr := bus.Close()
		require.True(t, closeErr == nil || errors.Is(closeErr, natsgo.ErrBadSubscription))
	})
	started := make(chan struct{})
	sub, err := bus.Subscribe(context.Background(), "yola.stats.native", func(ctx context.Context, _ event.Event) {
		close(started)
		<-ctx.Done()
	})
	require.NoError(t, err)
	observer := sub.(event.SubscriptionStatsProvider)
	require.NoError(t, bus.conn.Publish("yola.stats.native", nil))
	waitSignal(t, started, "handler did not start")
	for range 2 {
		require.NoError(t, bus.conn.Publish("yola.stats.native", nil))
	}
	require.NoError(t, bus.conn.Flush())
	require.Eventually(t, func() bool { return observer.SubscriptionStats().QueueDropped == 1 }, time.Second, time.Millisecond)
	require.NoError(t, sub.(*subscription).native.Unsubscribe())
	stats := observer.SubscriptionStats()
	require.False(t, stats.QueueDroppedCurrent)
	require.Equal(t, uint64(1), stats.QueueDropped)
	require.ErrorIs(t, sub.Unsubscribe(context.Background()), natsgo.ErrBadSubscription)
	require.True(t, observer.SubscriptionStats().Closed)
}

func TestSubscriptionStatsPayloadAndPanic(t *testing.T) {
	bus := newTestBus(t, startTestServer(t), WithMaxPayloadBytes(16))
	sub, err := bus.Subscribe(context.Background(), "yola.stats.payload", func(context.Context, event.Event) { panic("test") })
	require.NoError(t, err)
	observer, ok := sub.(event.SubscriptionStatsProvider)
	require.True(t, ok, "NATS subscription must expose local capacity stats")
	for _, size := range []int{17, 16, 0} {
		require.NoError(t, bus.conn.Publish("yola.stats.payload", make([]byte, size)))
	}
	require.NoError(t, bus.conn.Flush())
	require.Eventually(t, func() bool { return observer.SubscriptionStats().HandlerCalls == 2 }, time.Second, time.Millisecond)
	require.NoError(t, bus.Close())
	stats := observer.SubscriptionStats()
	require.Equal(t, uint64(1), stats.PayloadDropped)
	require.Equal(t, uint64(2), stats.HandlerPanics)
	require.Zero(t, stats.QueueDropped)
	require.True(t, stats.Closed)
	require.False(t, stats.QueueDroppedCurrent)
}
