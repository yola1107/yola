package nats

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yola/event"

	"github.com/nats-io/nats-server/v2/server"
	natsgo "github.com/nats-io/nats.go"
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
	t.Helper()
	return startTestServerInstance(t).ClientURL()
}

func startTestServerInstance(t testing.TB) *server.Server {
	t.Helper()
	return startTestServerWithOptions(t, testServerOptions())
}

func testServerOptions() *server.Options {
	return &server.Options{Host: "127.0.0.1", Port: server.RANDOM_PORT, NoLog: true, NoSigs: true}
}

func startTestServerWithOptions(t testing.TB, options *server.Options) *server.Server {
	t.Helper()
	natsServer, err := server.NewServer(options)
	require.NoError(t, err)
	t.Cleanup(func() {
		natsServer.Shutdown()
		natsServer.WaitForShutdown()
	})
	go natsServer.Start()
	require.True(t, natsServer.ReadyForConnections(5*time.Second), "NATS server did not become ready")
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

func testSubscription(t testing.TB, handle event.Subscription) *subscription {
	t.Helper()
	registered, ok := handle.(*subscription)
	require.True(t, ok, "NATS adapter returned an unexpected subscription: %T", handle)
	return registered
}

func newBlockedWriteBus(t *testing.T) (*Bus, *blockedWriteConn, func()) {
	t.Helper()
	dialer := &blockedWriteDialer{}
	conn, err := natsgo.Connect(
		startTestServer(t), natsgo.SetCustomDialer(dialer), natsgo.NoReconnect(),
	)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	bus := &Bus{
		ctx:             ctx,
		cancel:          cancel,
		conn:            conn,
		timeout:         time.Second,
		queueCapacity:   4,
		maxPayloadBytes: 64 << 10,
	}
	bus.startPublisher()
	release := sync.OnceFunc(func() { close(dialer.conn.release) })
	t.Cleanup(func() {
		release()
		require.NoError(t, bus.Close())
	})
	return bus, dialer.conn, release
}

// blockedWriteConn 在握手完成后阻塞一次原生写操作，清理时必须放行。
type blockedWriteConn struct {
	net.Conn
	armed   atomic.Bool
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *blockedWriteConn) Write(payload []byte) (int, error) {
	if c.armed.Load() {
		c.once.Do(func() { close(c.entered) })
		<-c.release
	}
	return c.Conn.Write(payload)
}

type blockedWriteDialer struct {
	conn *blockedWriteConn
}

func (d *blockedWriteDialer) Dial(network, address string) (net.Conn, error) {
	conn, err := net.DialTimeout(network, address, time.Second)
	if err != nil {
		return nil, err
	}
	d.conn = &blockedWriteConn{Conn: conn, entered: make(chan struct{}), release: make(chan struct{})}
	return d.conn, nil
}
