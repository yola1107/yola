package nats

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yola/event"

	natsgo "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

func TestUnsubscribeDeadlineIncludesNativeCleanup(t *testing.T) {
	bus, writer, unblockWrite := newBlockedWriteBus(t)
	handlerRelease := make(chan struct{})
	unblockHandler := sync.OnceFunc(func() { close(handlerRelease) })
	t.Cleanup(func() {
		unblockWrite()
		unblockHandler()
		require.NoError(t, bus.Close())
	})
	started := make(chan struct{})
	canceled := make(chan struct{})
	handle, err := bus.Subscribe(context.Background(), "yola.event.blocked-unsubscribe", func(ctx context.Context, _ event.Event) {
		close(started)
		<-ctx.Done()
		close(canceled)
		<-handlerRelease
	})
	require.NoError(t, err)
	require.NoError(t, bus.Publish(context.Background(), event.Event{Topic: "yola.event.blocked-unsubscribe"}))
	waitSignal(t, started, "handler did not start")
	writer.armed.Store(true)
	published := make(chan error, 1)
	go func() {
		published <- bus.Publish(context.Background(), event.Event{Topic: "yola.event.blocked-unsubscribe", Payload: make([]byte, 64<<10)})
	}()
	waitSignal(t, writer.entered, "writer did not reach the barrier")
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer stopCancel()
	unsubscribed := make(chan error, 1)
	go func() { unsubscribed <- handle.Unsubscribe(stopCtx) }()
	select {
	case err := <-unsubscribed:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(time.Second):
		t.Fatal("Unsubscribe exceeded its budget while native cleanup was blocked")
	}
	waitSignal(t, canceled, "handler was not canceled before native cleanup finished")
	select {
	case <-testSubscription(t, handle).stopDone:
		t.Fatal("subscription completed before native cleanup and handler exit")
	default:
	}
	unblockWrite()
	unblockHandler()
	require.NoError(t, handle.Unsubscribe(context.Background()))
	require.NoError(t, <-published)
}

func TestUnsubscribeCancelsAndWaitsForHandler(t *testing.T) {
	bus := newTestBus(t, startTestServer(t))
	started := make(chan struct{})
	finished := make(chan struct{})
	subscription, err := bus.Subscribe(context.Background(), "yola.event.cancel", func(ctx context.Context, _ event.Event) {
		close(started)
		<-ctx.Done()
		close(finished)
	})
	require.NoError(t, err)
	require.NoError(t, bus.Publish(context.Background(), event.Event{Topic: "yola.event.cancel"}))
	waitSignal(t, started, "handler did not start")

	require.NoError(t, subscription.Unsubscribe(context.Background()))
	waitSignal(t, finished, "unsubscribe returned before the handler finished")
}

func TestSubscribeReclaimsCompletedSubscriptions(t *testing.T) {
	bus := newTestBus(t, startTestServer(t))
	for range 16 {
		handle, err := bus.Subscribe(context.Background(), "yola.event.repeated", func(context.Context, event.Event) {})
		require.NoError(t, err)
		require.NoError(t, handle.Unsubscribe(context.Background()))
		require.Nil(t, testSubscription(t, handle).handler, "completed subscription must release its handler's captured state")
	}
	require.Len(t, bus.subscriptions, 1, "registration must not accumulate completed successful subscriptions")
}

func TestSubscribePreservesCompletedSubscriptionErrors(t *testing.T) {
	bus, err := New(WithURL(startTestServer(t)))
	require.NoError(t, err)
	stopErr := errors.New("unsubscribe failed")
	t.Cleanup(func() { require.ErrorIs(t, bus.Close(), stopErr) })
	failed := &subscription{deactivated: make(chan struct{}), stopDone: make(chan struct{}), stopErr: stopErr}
	failed.stopOnce.Do(func() {
		close(failed.deactivated)
		close(failed.stopDone)
	})
	bus.subscriptions = append(bus.subscriptions, failed)

	handle, err := bus.Subscribe(context.Background(), "yola.event.after-stop-error", func(context.Context, event.Event) {})
	require.NoError(t, err)
	require.NoError(t, handle.Unsubscribe(context.Background()))
	require.ErrorIs(t, bus.Close(), stopErr)
}

func TestUnsubscribeDoesNotWaitForBusOperations(t *testing.T) {
	bus := newTestBus(t, startTestServer(t))
	subscription, err := bus.Subscribe(context.Background(), "yola.event.independent-unsubscribe", func(context.Context, event.Event) {})
	require.NoError(t, err)

	bus.registrationMu.Lock()
	defer bus.registrationMu.Unlock()
	unsubscribed := make(chan error, 1)
	go func() { unsubscribed <- subscription.Unsubscribe(context.Background()) }()
	select {
	case err = <-unsubscribed:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("Unsubscribe waited for an unrelated Bus operation")
	}
}

func TestPublishDoesNotWaitForSubscriptionRegistration(t *testing.T) {
	bus := newTestBus(t, startTestServer(t))
	bus.registrationMu.Lock()
	defer bus.registrationMu.Unlock()

	published := make(chan error, 1)
	go func() {
		published <- bus.Publish(context.Background(), event.Event{Topic: "yola.event.publish-during-registration"})
	}()
	select {
	case err := <-published:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("Publish waited for subscription registration")
	}
}

func TestCloseRejectsSubscriptionWaitingForRegistration(t *testing.T) {
	bus := newTestBus(t, startTestServer(t))
	bus.registrationMu.Lock()
	subscribed := make(chan error, 1)
	go func() {
		_, err := bus.Subscribe(context.Background(), "yola.event.close-registration", func(context.Context, event.Event) {})
		subscribed <- err
	}()
	closed := make(chan error, 1)
	go func() { closed <- bus.Close() }()
	require.Eventually(t, func() bool { return bus.ctx.Err() != nil }, time.Second, time.Millisecond)
	bus.registrationMu.Unlock()

	require.ErrorIs(t, <-subscribed, event.ErrClosed)
	require.NoError(t, <-closed)
}

func TestBusCloseWaitsForUnsubscribingHandler(t *testing.T) {
	bus := newTestBus(t, startTestServer(t))
	started := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(unblock)
	subscription, err := bus.Subscribe(context.Background(), "yola.event.concurrent-close", func(ctx context.Context, _ event.Event) {
		close(started)
		<-ctx.Done()
		close(canceled)
		<-release
	})
	require.NoError(t, err)
	require.NoError(t, bus.Publish(context.Background(), event.Event{Topic: "yola.event.concurrent-close"}))
	waitSignal(t, started, "handler did not start")

	unsubscribed := make(chan error, 1)
	go func() { unsubscribed <- subscription.Unsubscribe(context.Background()) }()
	waitSignal(t, canceled, "unsubscribe did not cancel the handler")
	_, err = bus.Subscribe(context.Background(), "yola.event.during-cancel", func(context.Context, event.Event) {})
	require.NoError(t, err)
	stopped := make(chan error, 1)
	go func() { stopped <- bus.Close() }()
	select {
	case err := <-stopped:
		t.Fatalf("Bus.Close returned before the unsubscribing handler finished: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	unblock()
	select {
	case err := <-unsubscribed:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("Unsubscribe did not finish")
	}
	select {
	case err := <-stopped:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("Bus.Close did not wait for the unsubscribing handler")
	}
}

func TestUnsubscribeStopsDelivery(t *testing.T) {
	bus := newTestBus(t, startTestServer(t))
	received := make(chan struct{}, 1)
	subscription, err := bus.Subscribe(context.Background(), "yola.event.canceled", func(context.Context, event.Event) {
		received <- struct{}{}
	})
	require.NoError(t, err)
	require.NoError(t, subscription.Unsubscribe(context.Background()))
	require.NoError(t, bus.Publish(context.Background(), event.Event{Topic: "yola.event.canceled"}))
	require.NoError(t, bus.conn.Flush())
	select {
	case <-received:
		t.Fatal("unsubscribed handler received an event")
	case <-time.After(20 * time.Millisecond):
	}
}

func TestUnsubscribeTimeoutCanBeWaitedAgain(t *testing.T) {
	bus := newTestBus(t, startTestServer(t))
	started := make(chan struct{})
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(unblock)
	subscription, err := bus.Subscribe(context.Background(), "yola.event.slow", func(context.Context, event.Event) {
		close(started)
		<-release
	})
	require.NoError(t, err)
	require.NoError(t, bus.Publish(context.Background(), event.Event{Topic: "yola.event.slow"}))
	waitSignal(t, started, "handler did not start")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	require.ErrorIs(t, subscription.Unsubscribe(ctx), context.DeadlineExceeded)
	cancel()

	stopped := make(chan error, 1)
	go func() { stopped <- subscription.Unsubscribe(context.Background()) }()
	select {
	case err := <-stopped:
		t.Fatalf("second Unsubscribe returned before the handler finished: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	unblock()
	select {
	case err := <-stopped:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("second Unsubscribe did not observe shutdown completion")
	}
}

func TestSubscriptionStatsBacklogAndClose(t *testing.T) {
	bus := newTestBus(t, startTestServer(t), WithQueueCapacity(2), WithMaxPayloadBytes(16))
	started := make(chan struct{})
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
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
	for range 2 {
		require.NoError(t, bus.conn.Publish("yola.stats.backlog", make([]byte, 32)))
	}
	require.NoError(t, bus.conn.Flush())
	require.Eventually(t, func() bool { return observer.SubscriptionStats().QueueDepth == 2 }, time.Second, time.Millisecond)
	publishCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, bus.Publish(publishCtx, event.Event{Topic: "yola.stats.backlog", Payload: make([]byte, 16)}))
	for range 2 {
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
	require.ErrorIs(t, bus.conn.LastError(), natsgo.ErrSlowConsumer)
	_, err = bus.Subscribe(context.Background(), "yola.event.after-drop", func(context.Context, event.Event) {})
	require.NoError(t, err, "queue overflow must not stop subscription registration")
	// 留出计时器分辨率；拥塞交错仍由 started/release 屏障控制。
	<-time.After(5 * time.Millisecond)

	readDone := make(chan struct{})
	var readers sync.WaitGroup
	stopReaders := sync.OnceFunc(func() {
		close(readDone)
		readers.Wait()
	})
	t.Cleanup(stopReaders)
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
	testSubscription(t, sub).beginStop()
	unblock()
	require.NoError(t, sub.Unsubscribe(context.Background()))
	stopReaders()
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
	observer, ok := sub.(event.SubscriptionStatsProvider)
	require.True(t, ok)
	require.NoError(t, bus.conn.Publish("yola.stats.native", nil))
	waitSignal(t, started, "handler did not start")
	for range 2 {
		require.NoError(t, bus.conn.Publish("yola.stats.native", nil))
	}
	require.NoError(t, bus.conn.Flush())
	require.Eventually(t, func() bool { return observer.SubscriptionStats().QueueDropped == 1 }, time.Second, time.Millisecond)
	require.NoError(t, testSubscription(t, sub).native.Unsubscribe())
	stats := observer.SubscriptionStats()
	require.False(t, stats.QueueDroppedCurrent)
	require.Equal(t, uint64(1), stats.QueueDropped)
	require.ErrorIs(t, sub.Unsubscribe(context.Background()), natsgo.ErrBadSubscription)
	require.True(t, observer.SubscriptionStats().Closed)
}

func TestSubscriptionStatsPayloadAndPanic(t *testing.T) {
	url := startTestServer(t)
	bus := newTestBus(t, url, WithMaxPayloadBytes(16))
	var calls atomic.Int32
	sub, err := bus.Subscribe(context.Background(), "yola.stats.payload", func(context.Context, event.Event) {
		if calls.Add(1) <= 2 {
			panic("test")
		}
	})
	require.NoError(t, err)
	observer, ok := sub.(event.SubscriptionStatsProvider)
	require.True(t, ok, "NATS subscription must expose local capacity stats")
	publisher, err := natsgo.Connect(url)
	require.NoError(t, err)
	t.Cleanup(publisher.Close)
	require.NoError(t, publisher.Publish("yola.stats.payload", make([]byte, 17)))
	require.NoError(t, publisher.Flush())
	require.Eventually(t, func() bool { return observer.SubscriptionStats().PayloadDropped == 1 }, time.Second, time.Millisecond)
	require.Zero(t, calls.Load(), "oversized payload must not reach the handler")
	for _, size := range []int{16, 0, 0} {
		require.NoError(t, bus.Publish(context.Background(), event.Event{Topic: "yola.stats.payload", Payload: make([]byte, size)}))
	}
	require.NoError(t, bus.conn.Flush())
	require.Eventually(t, func() bool { return observer.SubscriptionStats().HandlerCalls == 3 }, time.Second, time.Millisecond)
	require.NoError(t, bus.Close())
	stats := observer.SubscriptionStats()
	require.Equal(t, uint64(1), stats.PayloadDropped)
	require.Equal(t, uint64(3), stats.HandlerCalls)
	require.Equal(t, uint64(2), stats.HandlerPanics)
	require.Zero(t, stats.QueueDropped)
	require.True(t, stats.Closed)
	require.False(t, stats.QueueDroppedCurrent)
}
