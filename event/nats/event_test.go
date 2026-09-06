package nats

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"yola/event"

	natsgo "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

func TestBusDefaults(t *testing.T) {
	bus, err := New(WithURL(startTestServer(t)))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, bus.Close()) })

	require.Equal(t, 5*time.Second, bus.timeout)
	require.Equal(t, 256, bus.queueCapacity)
	require.Equal(t, 64<<10, bus.maxPayloadBytes)
}

func TestBusPublishSubscribe(t *testing.T) {
	bus := newTestBus(t, startTestServer(t))
	received := make(chan event.Event, 1)

	subscription, err := bus.Subscribe(context.Background(), "yola.event.test", func(_ context.Context, incoming event.Event) {
		received <- incoming
	})
	require.NoError(t, err)
	require.NoError(t, bus.Publish(context.Background(), event.Event{
		Topic: "yola.event.test", Payload: []byte("hello yola"),
	}))

	incoming := waitEvent(t, received)
	require.Equal(t, "yola.event.test", incoming.Topic)
	require.Equal(t, []byte("hello yola"), incoming.Payload)
	require.NoError(t, subscription.Unsubscribe(context.Background()))
	require.NoError(t, subscription.Unsubscribe(context.Background()))
}

func TestBusFansOutToEverySubscriber(t *testing.T) {
	url := startTestServer(t)
	first := newTestBus(t, url)
	second := newTestBus(t, url)
	received := make(chan string, 2)

	_, err := first.Subscribe(context.Background(), "yola.event.broadcast", func(context.Context, event.Event) {
		received <- "first"
	})
	require.NoError(t, err)
	_, err = second.Subscribe(context.Background(), "yola.event.broadcast", func(context.Context, event.Event) {
		received <- "second"
	})
	require.NoError(t, err)
	require.NoError(t, first.Publish(context.Background(), event.Event{Topic: "yola.event.broadcast"}))

	actual := map[string]bool{}
	for range 2 {
		select {
		case name := <-received:
			actual[name] = true
		case <-time.After(time.Second):
			t.Fatal("broadcast did not reach every subscriber")
		}
	}
	require.Equal(t, map[string]bool{"first": true, "second": true}, actual)
}

func TestBusConstructionReportsConnectionFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	_, err = New(WithContext(ctx), WithURL(fmt.Sprintf("nats://127.0.0.1:%d", port)), WithTimeout(time.Second))
	require.Error(t, err)
	cancel()

	newTestServer(t, port)
	bus, err := New(WithURL(fmt.Sprintf("nats://127.0.0.1:%d", port)), WithTimeout(time.Second))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, bus.Close()) })
	require.NoError(t, bus.Publish(context.Background(), event.Event{Topic: "yola.event.retry"}))
}

func TestBusConstructionHonorsCancellationDuringConnect(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, listener.Close()) })
	accepted := make(chan net.Conn, 1)
	acceptErr := make(chan error, 1)
	go func() {
		conn, acceptError := listener.Accept()
		if acceptError != nil {
			acceptErr <- acceptError
			return
		}
		accepted <- conn
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	created := make(chan error, 1)
	go func() {
		bus, createErr := New(WithContext(ctx), WithURL("nats://"+listener.Addr().String()), WithTimeout(time.Second))
		if bus != nil {
			_ = bus.Close()
		}
		created <- createErr
	}()
	var serverConn net.Conn
	select {
	case serverConn = <-accepted:
	case err = <-acceptErr:
		t.Fatalf("accept NATS connection: %v", err)
	case <-time.After(time.Second):
		t.Fatal("NATS connection was not accepted")
	}
	t.Cleanup(func() { _ = serverConn.Close() })

	cancel()
	select {
	case err = <-created:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(100 * time.Millisecond):
		_ = serverConn.Close()
		err = <-created
		t.Fatalf("New did not return promptly after cancellation: %v", err)
	}
}

func TestBusContextCancellationClosesBusAndSubscriptions(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	bus, err := New(WithContext(ctx), WithURL(startTestServer(t)))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, bus.Close()) })

	started := make(chan struct{})
	stopped := make(chan struct{})
	_, err = bus.Subscribe(context.Background(), "yola.event.bus-context", func(handlerCtx context.Context, _ event.Event) {
		close(started)
		<-handlerCtx.Done()
		close(stopped)
	})
	require.NoError(t, err)
	require.NoError(t, bus.Publish(context.Background(), event.Event{Topic: "yola.event.bus-context"}))
	waitSignal(t, started, "handler did not start")

	cancel()
	waitSignal(t, stopped, "Bus context cancellation did not stop the handler")
	require.Eventually(t, func() bool {
		return errors.Is(bus.Publish(context.Background(), event.Event{Topic: "yola.event.bus-context"}), event.ErrClosed)
	}, time.Second, time.Millisecond)
	_, err = bus.Subscribe(context.Background(), "yola.event.after-bus-context", func(context.Context, event.Event) {})
	require.ErrorIs(t, err, event.ErrClosed)
	require.NoError(t, bus.Close())
}

func TestBusLifecycle(t *testing.T) {
	bus := newTestBus(t, startTestServer(t))
	published := event.Event{Topic: "yola.event.lifecycle"}
	_, err := bus.Subscribe(context.Background(), published.Topic, func(context.Context, event.Event) {})
	require.NoError(t, err)
	require.NoError(t, bus.Publish(context.Background(), published))
	require.NoError(t, bus.Close())
	require.NoError(t, bus.Close())
	require.ErrorIs(t, bus.Publish(context.Background(), published), event.ErrClosed)
	_, err = bus.Subscribe(context.Background(), published.Topic, func(context.Context, event.Event) {})
	require.ErrorIs(t, err, event.ErrClosed)
}

func TestReconnectBufferIsDisabled(t *testing.T) {
	natsServer := startTestServerInstance(t)
	bus := newTestBus(t, natsServer.ClientURL())
	natsServer.Shutdown()
	natsServer.WaitForShutdown()
	require.Eventually(t, func() bool {
		return bus.conn.Status() == natsgo.RECONNECTING
	}, time.Second, time.Millisecond)
	err := bus.Publish(context.Background(), event.Event{Topic: "yola.event.disconnected"})
	require.ErrorIs(t, err, natsgo.ErrReconnectBufExceeded)
}

func TestNewRejectsInvalidConfiguration(t *testing.T) {
	for _, test := range []struct {
		name   string
		option Option
	}{
		{name: "timeout", option: WithTimeout(0)},
		{name: "queue capacity", option: WithQueueCapacity(0)},
		{name: "payload size", option: WithMaxPayloadBytes(0)},
		{name: "TLS", option: WithTLS(nil)},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := New(test.option)
			require.Error(t, err)
		})
	}

	var nilCtx context.Context
	_, err := New(WithContext(nilCtx))
	require.ErrorIs(t, err, event.ErrInvalidContext)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = New(WithContext(canceled))
	require.ErrorIs(t, err, context.Canceled)
}

func TestBusRejectsInvalidInput(t *testing.T) {
	var nilCtx context.Context
	bus := newTestBus(t, startTestServer(t), WithMaxPayloadBytes(4))
	_, err := bus.Subscribe(context.Background(), "", func(context.Context, event.Event) {})
	require.ErrorIs(t, err, event.ErrInvalidTopic)
	_, err = bus.Subscribe(context.Background(), "yola.event.test", nil)
	require.ErrorIs(t, err, event.ErrInvalidHandler)
	_, err = bus.Subscribe(nilCtx, "yola.event.test", func(context.Context, event.Event) {})
	require.ErrorIs(t, err, event.ErrInvalidContext)
	_, err = bus.Subscribe(context.Background(), "yola.event.valid", func(context.Context, event.Event) {})
	require.NoError(t, err)
	require.ErrorIs(t, bus.Publish(nilCtx, event.Event{Topic: "yola.event.test"}), event.ErrInvalidContext)
	require.ErrorIs(t, bus.Publish(context.Background(), event.Event{}), event.ErrInvalidTopic)
	require.EqualError(t, bus.Publish(context.Background(), event.Event{
		Topic: "yola.event.test", Payload: []byte("12345"),
	}), "nats: payload exceeds 4 bytes")
}
