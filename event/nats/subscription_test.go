package nats

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yola/event"

	"github.com/nats-io/nats-server/v2/server"
	natsgo "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

func TestBusSubscribeRejectsDeniedTopic(t *testing.T) {
	options := testServerOptions()
	options.Users = []*server.User{{
		Username: "subscriber",
		Password: "secret",
		Permissions: &server.Permissions{
			Subscribe: &server.SubjectPermission{Allow: []string{"yola.event.allowed"}},
		},
	}}
	natsServer := startTestServerWithOptions(t, options)
	bus, err := New(
		WithURL(natsServer.ClientURL()),
		WithTimeout(time.Second),
		WithUserInfo("subscriber", "secret"),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, bus.Close()) })

	_, err = bus.Subscribe(context.Background(), "yola.event.denied", func(context.Context, event.Event) {})
	require.ErrorIs(t, err, natsgo.ErrPermissionViolation)
}

func TestSubscribeFailureRemainsTerminalAfterReconnect(t *testing.T) {
	options := testServerOptions()
	options.Users = []*server.User{{
		Username: "subscriber",
		Password: "secret",
		Permissions: &server.Permissions{
			Subscribe: &server.SubjectPermission{Allow: []string{"yola.event.allowed.>"}},
		},
	}}
	natsServer := startTestServerWithOptions(t, options)
	bus, err := New(
		WithURL(natsServer.ClientURL()),
		WithTimeout(time.Second),
		WithUserInfo("subscriber", "secret"),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, bus.Close()) })
	received := make(chan event.Event, 1)
	_, err = bus.Subscribe(context.Background(), "yola.event.allowed.first", func(_ context.Context, receivedEvent event.Event) {
		received <- receivedEvent
	})
	require.NoError(t, err)

	_, err = bus.Subscribe(context.Background(), "yola.event.denied", func(context.Context, event.Event) {})
	require.ErrorIs(t, err, natsgo.ErrPermissionViolation)
	require.NoError(t, bus.conn.ForceReconnect())
	require.Eventually(t, func() bool {
		return bus.conn.Status() == natsgo.CONNECTED && bus.conn.LastError() == nil
	}, time.Second, time.Millisecond)

	_, err = bus.Subscribe(context.Background(), "yola.event.allowed.second", func(context.Context, event.Event) {})
	require.ErrorIs(t, err, natsgo.ErrPermissionViolation)
	require.NoError(t, bus.Publish(context.Background(), event.Event{Topic: "yola.event.allowed.first"}))
	require.Equal(t, "yola.event.allowed.first", waitEvent(t, received).Topic)
}

func TestBusSubscribeRejectsMaximumSubscriptionsExceeded(t *testing.T) {
	options := testServerOptions()
	options.MaxSubs = 1
	natsServer := startTestServerWithOptions(t, options)
	bus := newTestBus(t, natsServer.ClientURL())
	_, err := bus.Subscribe(context.Background(), "yola.event.first", func(context.Context, event.Event) {})
	require.NoError(t, err)

	_, err = bus.Subscribe(context.Background(), "yola.event.second", func(context.Context, event.Event) {})
	require.ErrorIs(t, err, natsgo.ErrMaxSubscriptionsExceeded)
}

func TestSubscriptionActivationErrorIgnoresPublishPermissionFailure(t *testing.T) {
	publishErr := fmt.Errorf(
		"%w: Permissions Violation for Publish to %q",
		natsgo.ErrPermissionViolation,
		"yola.event.publish.denied",
	)
	require.NoError(t, subscriptionActivationError(nil, publishErr))

	subscribeErr := fmt.Errorf(
		"%w: Permissions Violation for Subscription to %q",
		natsgo.ErrPermissionViolation,
		"yola.event.subscribe.denied",
	)
	require.ErrorIs(t, subscriptionActivationError(nil, subscribeErr), natsgo.ErrPermissionViolation)
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

func TestUnsubscribeReleasesHandler(t *testing.T) {
	bus := newTestBus(t, startTestServer(t))
	handle, err := bus.Subscribe(context.Background(), "yola.event.release", func(context.Context, event.Event) {})
	require.NoError(t, err)

	require.NoError(t, handle.Unsubscribe(context.Background()))
	require.Nil(t, handle.(*subscription).handler, "completed subscription must release its handler's captured state")
}

func TestSubscribeReclaimsCompletedSubscriptions(t *testing.T) {
	bus := newTestBus(t, startTestServer(t))
	for range 16 {
		handle, err := bus.Subscribe(context.Background(), "yola.event.repeated", func(context.Context, event.Event) {})
		require.NoError(t, err)
		require.NoError(t, handle.Unsubscribe(context.Background()))
	}
	require.Len(t, bus.subscriptions, 1, "registration must not accumulate completed successful subscriptions")
}

func TestSubscribePreservesCompletedSubscriptionErrors(t *testing.T) {
	bus, err := New(WithURL(startTestServer(t)))
	require.NoError(t, err)
	stopErr := errors.New("unsubscribe failed")
	t.Cleanup(func() { require.ErrorIs(t, bus.Close(), stopErr) })
	failed := &subscription{stopDone: make(chan struct{}), stopErr: stopErr}
	failed.stopOnce.Do(func() { close(failed.stopDone) })
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
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
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

	releaseOnce.Do(func() { close(release) })
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
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
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
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-stopped:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("second Unsubscribe did not observe shutdown completion")
	}
}

func TestFullSubscriptionQueueDropsWithoutBlocking(t *testing.T) {
	bus := newTestBus(t, startTestServer(t), WithQueueCapacity(1))
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	var once sync.Once
	handle, err := bus.Subscribe(context.Background(), "yola.event.drop", func(context.Context, event.Event) {
		once.Do(func() { close(started) })
		<-release
	})
	require.NoError(t, err)
	require.NoError(t, bus.Publish(context.Background(), event.Event{Topic: "yola.event.drop"}))
	waitSignal(t, started, "handler did not start")
	for range 64 {
		require.NoError(t, bus.Publish(context.Background(), event.Event{Topic: "yola.event.drop"}))
	}
	require.NoError(t, bus.conn.Flush())

	native := handle.(*subscription).native
	require.Eventually(t, func() bool {
		dropped, dropErr := native.Dropped()
		return dropErr == nil && dropped > 0
	}, time.Second, 10*time.Millisecond)
	require.ErrorIs(t, bus.conn.LastError(), natsgo.ErrSlowConsumer)
	_, err = bus.Subscribe(context.Background(), "yola.event.after-drop", func(context.Context, event.Event) {})
	require.NoError(t, err)
}

func TestSubscriptionDropsOversizedReceivedPayload(t *testing.T) {
	url := startTestServer(t)
	bus := newTestBus(t, url, WithMaxPayloadBytes(4))
	received := make(chan event.Event, 1)
	handle, err := bus.Subscribe(context.Background(), "yola.event.oversized", func(_ context.Context, incoming event.Event) {
		received <- incoming
	})
	require.NoError(t, err)

	publisher, err := natsgo.Connect(url)
	require.NoError(t, err)
	t.Cleanup(publisher.Close)
	require.NoError(t, publisher.Publish("yola.event.oversized", []byte("12345")))
	require.NoError(t, publisher.Flush())

	subscription := handle.(*subscription)
	require.Eventually(t, func() bool { return subscription.dropped.Load() == 1 }, time.Second, time.Millisecond)
	select {
	case incoming := <-received:
		t.Fatalf("received oversized payload: %+v", incoming)
	default:
	}
}

func TestHandlerPanicDoesNotStopSubscription(t *testing.T) {
	bus := newTestBus(t, startTestServer(t))
	var calls atomic.Int32
	done := make(chan struct{})
	_, err := bus.Subscribe(context.Background(), "yola.event.panic", func(context.Context, event.Event) {
		if calls.Add(1) == 1 {
			panic("test panic")
		}
		close(done)
	})
	require.NoError(t, err)
	require.NoError(t, bus.Publish(context.Background(), event.Event{Topic: "yola.event.panic"}))
	require.NoError(t, bus.Publish(context.Background(), event.Event{Topic: "yola.event.panic"}))
	waitSignal(t, done, "subscription stopped after handler panic")
}
