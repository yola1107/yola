package nats

import (
	"bufio"
	"context"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"yola/event"

	natsgo "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

func TestSubscribeCanceledWhileWaitingDoesNotActivate(t *testing.T) {
	bus, peer := newActivationTestBus(t)
	first := make(chan error, 1)
	go func() {
		_, err := bus.Subscribe(context.Background(), "yola.event.first", func(context.Context, event.Event) {})
		first <- err
	}()
	require.Equal(t, "SUB yola.event.first  1", peer.read(t))
	require.Equal(t, "PING", peer.read(t))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waiting := &registrationContext{Context: ctx, entered: make(chan struct{})}
	second := make(chan error, 1)
	go func() {
		_, err := bus.Subscribe(waiting, "yola.event.canceled", func(context.Context, event.Event) {})
		second <- err
	}()
	// 初次 context 校验已通过；第一注册仍持锁等待 PONG。
	waitSignal(t, waiting.entered, "second registration did not reach admission")
	cancel()
	peer.send(t, "PONG\r\n")
	require.NoError(t, <-first)
	require.ErrorIs(t, <-second, context.Canceled)
	require.NoError(t, bus.registrationErr, "cancellation before activation must not stop registration")

	third := make(chan error, 1)
	go func() {
		_, err := bus.Subscribe(context.Background(), "yola.event.third", func(context.Context, event.Event) {})
		third <- err
	}()
	// SID 连续且线上没有第二次 SUB/UNSUB，证明取消者未创建底层订阅。
	require.Equal(t, "SUB yola.event.third  2", peer.read(t))
	require.Equal(t, "PING", peer.read(t))
	peer.send(t, "PONG\r\n")
	require.NoError(t, <-third)
}

func TestSubscribeCancellationAfterActivationRemainsTerminal(t *testing.T) {
	bus, peer := newActivationTestBus(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	subscribed := make(chan error, 1)
	go func() {
		_, err := bus.Subscribe(ctx, "yola.event.canceled", func(context.Context, event.Event) {})
		subscribed <- err
	}()
	require.Equal(t, "SUB yola.event.canceled  1", peer.read(t))
	require.Equal(t, "PING", peer.read(t))
	cancel()
	require.ErrorIs(t, <-subscribed, context.Canceled)
	_, err := bus.Subscribe(context.Background(), "yola.event.later", func(context.Context, event.Event) {})
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, bus.conn.NumSubscriptions())

	reconnected := make(chan struct{})
	bus.conn.SetReconnectHandler(func(*natsgo.Conn) { close(reconnected) })
	require.NoError(t, bus.conn.ForceReconnect())
	peer = acceptActivationPeer(t, peer.listener)
	require.Equal(t, "PING", peer.read(t))
	peer.send(t, "PONG\r\n")
	waitSignal(t, reconnected, "NATS did not reconnect")
	_, err = bus.Subscribe(context.Background(), "yola.event.after-reconnect", func(context.Context, event.Event) {})
	require.ErrorIs(t, err, context.Canceled)
}

func TestSubscribeFlushTimeoutRemainsTerminal(t *testing.T) {
	bus, peer := newActivationTestBus(t)
	bus.timeout = 20 * time.Millisecond
	subscribed := make(chan error, 1)
	go func() {
		_, err := bus.Subscribe(context.Background(), "yola.event.timeout", func(context.Context, event.Event) {})
		subscribed <- err
	}()
	require.Equal(t, "SUB yola.event.timeout  1", peer.read(t))
	require.Equal(t, "PING", peer.read(t))
	require.ErrorIs(t, <-subscribed, context.DeadlineExceeded)
	_, err := bus.Subscribe(context.Background(), "yola.event.later", func(context.Context, event.Event) {})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Zero(t, bus.conn.NumSubscriptions())
}

func TestCloseCancelsActiveSubscription(t *testing.T) {
	bus, peer := newActivationTestBus(t)
	subscribed := make(chan error, 1)
	go func() {
		_, err := bus.Subscribe(context.Background(), "yola.event.closing", func(context.Context, event.Event) {})
		subscribed <- err
	}()
	require.Equal(t, "SUB yola.event.closing  1", peer.read(t))
	require.Equal(t, "PING", peer.read(t))
	closed := make(chan error, 1)
	go func() { closed <- bus.Close() }()
	require.ErrorIs(t, <-subscribed, event.ErrClosed)
	require.NoError(t, <-closed)
	_, err := bus.Subscribe(context.Background(), "yola.event.later", func(context.Context, event.Event) {})
	require.ErrorIs(t, err, event.ErrClosed)
}

type registrationContext struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (c *registrationContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered) })
	return c.Context.Done()
}

// activationPeer 只驱动激活所需的协议屏障，不模拟 broker 的权限决策。
type activationPeer struct {
	listener net.Listener
	conn     net.Conn
	reader   *bufio.Reader
}

func newActivationTestBus(t *testing.T) (*Bus, *activationPeer) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, listener.Close()) })
	require.NoError(t, listener.(*net.TCPListener).SetDeadline(time.Now().Add(5*time.Second)))
	type result struct {
		bus *Bus
		err error
	}
	created := make(chan result, 1)
	go func() {
		bus, createErr := New(WithURL("nats://"+listener.Addr().String()), WithTimeout(5*time.Second))
		created <- result{bus: bus, err: createErr}
	}()
	peer := acceptActivationPeer(t, listener)
	outcome := <-created
	require.NoError(t, outcome.err)
	t.Cleanup(func() { require.NoError(t, outcome.bus.Close()) })
	return outcome.bus, peer
}

func acceptActivationPeer(t *testing.T, listener net.Listener) *activationPeer {
	t.Helper()
	conn, err := listener.Accept()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
	peer := &activationPeer{listener: listener, conn: conn, reader: bufio.NewReader(conn)}
	peer.send(t, "INFO {\"server_id\":\"activation-test\",\"max_payload\":1048576}\r\n")
	require.True(t, strings.HasPrefix(peer.read(t), "CONNECT "))
	require.Equal(t, "PING", peer.read(t))
	peer.send(t, "PONG\r\n")
	return peer
}

func (p *activationPeer) read(t *testing.T) string {
	t.Helper()
	line, err := p.reader.ReadString('\n')
	require.NoError(t, err)
	return strings.TrimSuffix(line, "\r\n")
}

func (p *activationPeer) send(t *testing.T, protocol string) {
	t.Helper()
	_, err := io.WriteString(p.conn, protocol)
	require.NoError(t, err)
}
