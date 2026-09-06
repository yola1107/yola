package nats

import (
	"testing"
	"time"

	"yola/event"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/stretchr/testify/require"
)

func waitEvent(t *testing.T, events <-chan event.Event) event.Event {
	t.Helper()
	select {
	case incoming := <-events:
		return incoming
	case <-time.After(time.Second):
		t.Fatal("event was not delivered")
		return event.Event{}
	}
}

func waitSignal(t *testing.T, signal <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal(message)
	}
}

func startTestServer(t testing.TB) string {
	return startTestServerInstance(t).ClientURL()
}

func startTestServerInstance(t testing.TB) *server.Server {
	return startTestServerWithOptions(t, testServerOptions())
}

func newTestServer(t testing.TB, port int) *server.Server {
	options := testServerOptions()
	options.Port = port
	return startTestServerWithOptions(t, options)
}

func testServerOptions() *server.Options {
	return &server.Options{Host: "127.0.0.1", Port: server.RANDOM_PORT, NoLog: true, NoSigs: true}
}

func startTestServerWithOptions(t testing.TB, options *server.Options) *server.Server {
	t.Helper()
	natsServer, err := server.NewServer(options)
	require.NoError(t, err)
	go natsServer.Start()
	require.True(t, natsServer.ReadyForConnections(5*time.Second), "NATS server did not become ready")
	t.Cleanup(func() {
		natsServer.Shutdown()
		natsServer.WaitForShutdown()
	})
	return natsServer
}

func newTestBus(t testing.TB, url string, opts ...Option) *Bus {
	t.Helper()
	configured := make([]Option, 0, 2+len(opts))
	configured = append(configured, WithURL(url), WithTimeout(time.Second))
	configured = append(configured, opts...)
	bus, err := New(configured...)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, bus.Close()) })
	return bus
}
