package nats

import (
	"context"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"yola/event"

	"github.com/nats-io/nats-server/v2/server"
	natsgo "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

func TestSubscribeReportsPermissionFailureAsynchronously(t *testing.T) {
	logged := captureTransportErrors(t)
	options := testServerOptions()
	options.NoAuthUser = "activation-test"
	options.Users = []*server.User{{
		Username: "activation-test",
		Permissions: &server.Permissions{
			Subscribe: &server.SubjectPermission{Allow: []string{"yola.event.allowed.>"}},
			Publish:   &server.SubjectPermission{Allow: []string{"yola.event.allowed.>"}},
		},
	}}
	bus := newTestBus(t, startTestServerWithOptions(t, options).ClientURL())
	received := make(chan event.Event, 1)
	_, err := bus.Subscribe(context.Background(), "yola.event.allowed.first", func(_ context.Context, incoming event.Event) {
		received <- incoming
	})
	require.NoError(t, err)

	denied, err := bus.Subscribe(context.Background(), "yola.event.denied", func(context.Context, event.Event) {})
	require.NoError(t, err)
	failure := waitTransportError(t, logged)
	require.ErrorIs(t, failure.err, natsgo.ErrPermissionViolation)
	require.Empty(t, failure.topic, "NATS does not identify the subscription in an ACL callback")
	require.NoError(t, bus.Publish(context.Background(), event.Event{Topic: "yola.event.publish.denied"}))
	require.ErrorIs(t, waitTransportError(t, logged).err, natsgo.ErrPermissionViolation)
	_, err = bus.Subscribe(context.Background(), "yola.event.allowed.second", func(context.Context, event.Event) {})
	require.NoError(t, err)
	require.NoError(t, bus.Publish(context.Background(), event.Event{Topic: "yola.event.allowed.first"}))
	require.Equal(t, "yola.event.allowed.first", waitEvent(t, received).Topic)
	require.NoError(t, denied.Unsubscribe(context.Background()))
}

func TestSubscribeReportsMaximumSubscriptionsAsynchronously(t *testing.T) {
	logged := captureTransportErrors(t)
	options := testServerOptions()
	options.MaxSubs = 1
	bus := newTestBus(t, startTestServerWithOptions(t, options).ClientURL())
	first, err := bus.Subscribe(context.Background(), "yola.event.first", func(context.Context, event.Event) {})
	require.NoError(t, err)
	denied, err := bus.Subscribe(context.Background(), "yola.event.over-limit", func(context.Context, event.Event) {})
	require.NoError(t, err)
	failure := waitTransportError(t, logged)
	require.ErrorIs(t, failure.err, natsgo.ErrMaxSubscriptionsExceeded)
	require.Empty(t, failure.topic)
	require.NoError(t, denied.Unsubscribe(context.Background()))
	require.NoError(t, first.Unsubscribe(context.Background()))

	received := make(chan event.Event, 1)
	_, err = bus.Subscribe(context.Background(), "yola.event.later", func(_ context.Context, incoming event.Event) {
		received <- incoming
	})
	require.NoError(t, err)
	require.NoError(t, bus.Publish(context.Background(), event.Event{Topic: "yola.event.later"}))
	require.Equal(t, "yola.event.later", waitEvent(t, received).Topic)
}

func TestSubscribeReportsRepeatedPermissionFailureAfterReconnect(t *testing.T) {
	logged := captureTransportErrors(t)
	natsServer := startTestServerInstance(t)
	bus := newTestBus(t, natsServer.ClientURL())
	_, err := bus.Subscribe(context.Background(), "yola.event.denied", func(context.Context, event.Event) {})
	require.NoError(t, err)
	received := make(chan event.Event, 1)
	_, err = bus.Subscribe(context.Background(), "yola.event.allowed", func(_ context.Context, incoming event.Event) {
		received <- incoming
	})
	require.NoError(t, err)
	port := natsServer.Addr().(*net.TCPAddr).Port
	natsServer.Shutdown()
	natsServer.WaitForShutdown()

	options := testServerOptions()
	options.Port = port
	options.NoAuthUser = "activation-test"
	options.Users = []*server.User{{
		Username: "activation-test",
		Permissions: &server.Permissions{
			Subscribe: &server.SubjectPermission{Allow: []string{"yola.event.allowed"}},
		},
	}}
	startTestServerWithOptions(t, options)
	require.ErrorIs(t, waitTransportError(t, logged).err, natsgo.ErrPermissionViolation)
	_, err = bus.Subscribe(context.Background(), "yola.event.denied", func(context.Context, event.Event) {})
	require.NoError(t, err)
	require.ErrorIs(t, waitTransportError(t, logged).err, natsgo.ErrPermissionViolation)
	require.NoError(t, bus.Publish(context.Background(), event.Event{Topic: "yola.event.allowed"}))
	require.Equal(t, "yola.event.allowed", waitEvent(t, received).Topic)
}

func TestSubscribeAsyncErrorsAreReportedIndependently(t *testing.T) {
	for _, test := range []struct {
		name     string
		protocol string
		failures []transportError
	}{
		{
			name: "subscription ACL then publish ACL",
			protocol: "-ERR 'Permissions Violation for Subscription to \"yola.event.pending\"'\r\n" +
				"-ERR 'Permissions Violation for Publish to \"yola.event.unrelated\"'\r\n",
			failures: []transportError{{err: natsgo.ErrPermissionViolation}, {err: natsgo.ErrPermissionViolation}},
		},
		{
			name: "subscription ACL then slow consumer",
			protocol: "-ERR 'Permissions Violation for Subscription to \"yola.event.pending\"'\r\n" +
				"MSG yola.event.busy 1 1\r\na\r\nMSG yola.event.busy 1 1\r\nb\r\n",
			failures: []transportError{
				{err: natsgo.ErrPermissionViolation}, {err: natsgo.ErrSlowConsumer, topic: "yola.event.busy"},
			},
		},
		{
			name: "maximum subscriptions then publish ACL",
			protocol: "-ERR 'Maximum Subscriptions Exceeded'\r\n" +
				"-ERR 'Permissions Violation for Publish to \"yola.event.unrelated\"'\r\n",
			failures: []transportError{{err: natsgo.ErrMaxSubscriptionsExceeded}, {err: natsgo.ErrPermissionViolation}},
		},
		{
			name:     "other subscription ACL",
			protocol: "-ERR 'Permissions Violation for Subscription to \"yola.event.busy\"'\r\n",
			failures: []transportError{{err: natsgo.ErrPermissionViolation}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			logged := captureTransportErrors(t)
			bus, peer := newActivationTestBus(t)
			_, err := bus.conn.ChanSubscribe("yola.event.busy", make(chan *natsgo.Msg, 1))
			require.NoError(t, err)
			subscribed := make(chan error, 1)
			go func() {
				_, subscribeErr := bus.Subscribe(context.Background(), "yola.event.pending", func(context.Context, event.Event) {})
				subscribed <- subscribeErr
			}()
			require.Equal(t, "SUB yola.event.busy  1", peer.read(t))
			require.Equal(t, "SUB yola.event.pending  2", peer.read(t))
			require.Equal(t, "PING", peer.read(t))
			peer.send(t, test.protocol+"PONG\r\n")
			require.NoError(t, <-subscribed)
			for _, expected := range test.failures {
				failure := waitTransportError(t, logged)
				require.ErrorIs(t, failure.err, expected.err)
				require.Equal(t, expected.topic, failure.topic)
			}
		})
	}
}

func TestSubscribeDoesNotWaitForAsyncErrorReporting(t *testing.T) {
	logged := captureTransportErrors(t)
	bus, peer := newActivationTestBus(t)
	report := bus.conn.ErrorHandler()
	callbackStarted := make(chan struct{})
	callbackRelease := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(callbackRelease) }) })
	bus.conn.SetErrorHandler(func(conn *natsgo.Conn, sub *natsgo.Subscription, err error) {
		close(callbackStarted)
		<-callbackRelease
		report(conn, sub, err)
	})
	subscribed := make(chan error, 1)
	go func() {
		_, err := bus.Subscribe(context.Background(), "yola.event.pending", func(context.Context, event.Event) {})
		subscribed <- err
	}()
	require.Equal(t, "SUB yola.event.pending  1", peer.read(t))
	require.Equal(t, "PING", peer.read(t))
	peer.send(t, "-ERR 'Maximum Subscriptions Exceeded'\r\n")
	waitSignal(t, callbackStarted, "async error was not dispatched")
	peer.send(t, "PONG\r\n")
	require.NoError(t, <-subscribed)
	releaseOnce.Do(func() { close(callbackRelease) })
	require.ErrorIs(t, waitTransportError(t, logged).err, natsgo.ErrMaxSubscriptionsExceeded)
}

type transportError struct {
	err   error
	topic string
}

type transportErrorHandler struct {
	slog.Handler
	reports chan transportError
}

func (h transportErrorHandler) Handle(_ context.Context, record slog.Record) error {
	if record.Message != "event transport error" {
		return nil
	}
	var failure transportError
	record.Attrs(func(attr slog.Attr) bool {
		switch attr.Key {
		case "error":
			failure.err, _ = attr.Value.Any().(error)
		case "topic":
			failure.topic = attr.Value.String()
		}
		return true
	})
	h.reports <- failure
	return nil
}

func captureTransportErrors(t *testing.T) <-chan transportError {
	t.Helper()
	previous := slog.Default()
	reports := make(chan transportError, 16)
	slog.SetDefault(slog.New(transportErrorHandler{
		Handler: slog.NewTextHandler(io.Discard, nil), reports: reports,
	}))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return reports
}

func waitTransportError(t *testing.T, reports <-chan transportError) transportError {
	t.Helper()
	select {
	case failure := <-reports:
		return failure
	case <-time.After(5 * time.Second):
		t.Fatal("transport error was not reported")
		return transportError{}
	}
}
