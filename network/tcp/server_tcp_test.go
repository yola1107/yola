package tcp

import (
	"bufio"
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yola/api/protocol/v1"
	"yola/network"
)

type blockingOpenHandler struct {
	started  chan<- struct{}
	release  <-chan struct{}
	finished chan<- error
}

func (h blockingOpenHandler) Open(ctx context.Context, _ network.Connection) error {
	h.started <- struct{}{}
	select {
	case <-h.release:
		return nil
	case <-ctx.Done():
		if h.finished != nil {
			h.finished <- ctx.Err()
		}
		return ctx.Err()
	}
}

func (blockingOpenHandler) Handle(context.Context, network.Connection, *v1.Proto) (*v1.Proto, error) {
	return nil, nil
}

func (blockingOpenHandler) Close(context.Context, network.Connection) {}

func TestServerHandshakeDeadlineClosesIdleConnection(t *testing.T) {
	endpoint := startTCPTestServer(t, tcpTestHandler{}, HandshakeTimeout(40*time.Millisecond))
	conn := dialTCPTest(t, endpoint)
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("idle connection remained open after handshake deadline")
	}
}

func TestServerHandshakeDeadlineIncludesOpen(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	finished := make(chan error, 1)
	var releaseOnce sync.Once
	releaseOpen := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseOpen)
	endpoint := startTCPTestServer(t, blockingOpenHandler{started: started, release: release, finished: finished},
		HandshakeTimeout(40*time.Millisecond))
	conn := dialTCPTest(t, endpoint)
	defer conn.Close()
	<-started
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("connection remained open while Open exceeded handshake deadline")
	}
	select {
	case err := <-finished:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Open() context error = %v, want %v", err, context.DeadlineExceeded)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Open context was not canceled at the handshake deadline")
	}
	releaseOpen()
}

type handshakeDeadlineHandler struct {
	deadline chan<- time.Time
	release  <-chan struct{}
}

func (h handshakeDeadlineHandler) Open(ctx context.Context, _ network.Connection) error {
	deadline, _ := ctx.Deadline()
	h.deadline <- deadline
	<-h.release
	return nil
}

func (handshakeDeadlineHandler) Handle(context.Context, network.Connection, *v1.Proto) (*v1.Proto, error) {
	return nil, nil
}

func (handshakeDeadlineHandler) Close(context.Context, network.Connection) {}

type lateOpenHandler struct {
	started chan struct{}
	release chan struct{}
	closed  chan error
}

func (h lateOpenHandler) Open(context.Context, network.Connection) error {
	close(h.started)
	<-h.release
	return nil
}

func (lateOpenHandler) Handle(context.Context, network.Connection, *v1.Proto) (*v1.Proto, error) {
	return nil, nil
}

func (h lateOpenHandler) Close(ctx context.Context, _ network.Connection) {
	h.closed <- ctx.Err()
}

func TestServeTCPCloseRunsWhenOpenReturnsAfterHandshakeDeadline(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() { _ = clientConn.Close() })
	handler := lateOpenHandler{
		started: make(chan struct{}),
		release: make(chan struct{}),
		closed:  make(chan error, 1),
	}
	server := newTCPServer(t, handler, HandshakeTimeout(20*time.Millisecond))
	done := make(chan struct{})
	go func() {
		server.serveTCP(context.Background(), serverConn, "connection-id")
		close(done)
	}()
	waitTCPValue(t, handler.started)
	if err := clientConn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := clientConn.Read(make([]byte, 1)); err == nil {
		t.Fatal("connection remained open after handshake deadline")
	}
	close(handler.release)
	if err := waitTCPValue(t, handler.closed); !errors.Is(err, context.Canceled) {
		t.Fatalf("Close context error = %v, want %v", err, context.Canceled)
	}
	waitTCPValue(t, done)
}

type readDeadlineConn struct {
	net.Conn
	deadline chan<- time.Time
}

func (c *readDeadlineConn) SetReadDeadline(deadline time.Time) error {
	c.deadline <- deadline
	return c.Conn.SetReadDeadline(deadline)
}

func TestServeTCPReusesOpenHandshakeDeadlineForFirstRead(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	deadline := make(chan time.Time, 1)
	readDeadline := make(chan time.Time, 1)
	release := make(chan struct{})
	server := newTCPServer(t, handshakeDeadlineHandler{deadline: deadline, release: release},
		HandshakeTimeout(time.Second))
	done := make(chan struct{})
	go func() {
		server.serveTCP(context.Background(), &readDeadlineConn{Conn: serverConn, deadline: readDeadline}, "connection-id")
		close(done)
	}()
	openDeadline := <-deadline
	time.Sleep(20 * time.Millisecond)
	close(release)
	firstReadDeadline := <-readDeadline
	_ = clientConn.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("serveTCP did not stop after the connection closed")
	}
	if openDeadline.IsZero() {
		t.Fatal("Open context has no handshake deadline")
	}
	if delta := firstReadDeadline.Sub(openDeadline); delta < -time.Millisecond || delta > time.Millisecond {
		t.Fatalf("first read deadline differs from Open deadline by %v", delta)
	}
}

func TestServerHeartbeatExtendsReadDeadline(t *testing.T) {
	endpoint := startTCPTestServer(t, tcpTestHandler{},
		HandshakeTimeout(100*time.Millisecond), HeartbeatTimeout(400*time.Millisecond))
	conn := dialTCPTest(t, endpoint)
	defer conn.Close()
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	roundTripTCPProto(t, reader, writer, &v1.Proto{Op: v1.OpHeartbeat})

	time.Sleep(150 * time.Millisecond)
	reply := roundTripTCPProto(t, reader, writer, &v1.Proto{Op: v1.OpRequest, Seq: 7})
	if reply.Op != v1.OpResponse || reply.Seq != 7 {
		t.Fatalf("reply = %+v, want response sequence 7", reply)
	}

	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("connection remained open after heartbeat deadline")
	}
}

func roundTripTCPProto(t *testing.T, reader *bufio.Reader, writer *bufio.Writer, message *v1.Proto) *v1.Proto {
	t.Helper()
	if err := writeFrame(writer, defaultCodec(), message); err != nil {
		t.Fatal(err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	reply := new(v1.Proto)
	if err := readFrame(reader, defaultCodec(), reply); err != nil {
		t.Fatal(err)
	}
	return reply
}

type messageTimeoutHandler struct {
	timedOut chan<- error
}

func (*messageTimeoutHandler) Open(context.Context, network.Connection) error { return nil }

func (h *messageTimeoutHandler) Handle(ctx context.Context, _ network.Connection, message *v1.Proto) (*v1.Proto, error) {
	if message.Op == v1.OpAuth {
		return &v1.Proto{Op: v1.OpAuthReply, Seq: message.Seq}, nil
	}
	<-ctx.Done()
	h.timedOut <- ctx.Err()
	return nil, ctx.Err()
}

func (*messageTimeoutHandler) Close(context.Context, network.Connection) {}

func TestServerMessageTimeoutCancelsHandlerContext(t *testing.T) {
	timedOut := make(chan error, 1)
	endpoint := startTCPTestServer(t, &messageTimeoutHandler{timedOut: timedOut}, Timeout(20*time.Millisecond))
	client, err := NewClient(
		context.Background(),
		WithAddress(endpoint),
		WithServiceName("game"),
		WithToken("synthetic-token"),
	)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	t.Cleanup(client.Close)

	requestCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, _, err := client.Request(requestCtx, 1, new(v1.ClientAuthReq)); err == nil {
		t.Fatal("Client.Request() error = nil, want connection failure after handler timeout")
	}
	if err := waitTCPValue(t, timedOut); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("handler context error = %v, want %v", err, context.DeadlineExceeded)
	}
}

type acceptedConnectionHandler struct {
	opened chan struct{}
}

func (h *acceptedConnectionHandler) Open(context.Context, network.Connection) error {
	h.opened <- struct{}{}
	return nil
}

func (*acceptedConnectionHandler) Handle(_ context.Context, _ network.Connection, message *v1.Proto) (*v1.Proto, error) {
	return message, nil
}

func (*acceptedConnectionHandler) Close(context.Context, network.Connection) {}

type delayedTCPListener struct {
	net.Listener
	accepting chan struct{}
	release   <-chan struct{}
	accepted  atomic.Bool
	closes    atomic.Int32
}

func (l *delayedTCPListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	if l.accepted.CompareAndSwap(false, true) {
		close(l.accepting)
		<-l.release
	}
	return conn, nil
}

func (l *delayedTCPListener) Close() error {
	l.closes.Add(1)
	return l.Listener.Close()
}

func TestServerRejectsAcceptedConnectionAfterStop(t *testing.T) {
	baseListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = baseListener.Close() })
	release := make(chan struct{})
	releaseAccept := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseAccept)
	lis := &delayedTCPListener{
		Listener:  baseListener,
		accepting: make(chan struct{}),
		release:   release,
	}
	h := &acceptedConnectionHandler{opened: make(chan struct{}, 1)}
	s := newTCPServer(t, h, Listener(lis))
	serverDone := make(chan error, 1)
	go func() { serverDone <- s.Start(context.Background()) }()
	clientConn := dialTCPTest(t, baseListener.Addr().String())
	t.Cleanup(func() { _ = clientConn.Close() })
	waitTCPValue(t, lis.accepting)

	stopCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = s.Stop(stopCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Server.Stop() error = %v, want %v", err, context.Canceled)
	}
	releaseAccept()
	if err = waitTCPValue(t, serverDone); err != nil {
		t.Fatalf("Server.Start() error = %v", err)
	}
	if closes := lis.closes.Load(); closes != 1 {
		t.Fatalf("Listener.Close calls = %d, want 1", closes)
	}
	select {
	case <-h.opened:
		t.Fatal("ConnectionHandler.Open was called after Stop won connection tracking")
	default:
	}
}

type connectionLifecycleHandler struct {
	openErr error
	opened  chan struct{}
	closed  atomic.Int32
}

func (h *connectionLifecycleHandler) Open(context.Context, network.Connection) error {
	h.opened <- struct{}{}
	return h.openErr
}

func (*connectionLifecycleHandler) Handle(_ context.Context, _ network.Connection, message *v1.Proto) (*v1.Proto, error) {
	return message, nil
}

func (h *connectionLifecycleHandler) Close(context.Context, network.Connection) { h.closed.Add(1) }

type panicOpenHandler struct {
	opened chan struct{}
}

func (h panicOpenHandler) Open(context.Context, network.Connection) error {
	close(h.opened)
	panic("open panic")
}

func (panicOpenHandler) Handle(_ context.Context, _ network.Connection, message *v1.Proto) (*v1.Proto, error) {
	return message, nil
}

func (panicOpenHandler) Close(context.Context, network.Connection) {}

func TestOpenPanicDoesNotLeakConnection(t *testing.T) {
	h := panicOpenHandler{opened: make(chan struct{})}
	s := newTCPServer(t, h, Address("127.0.0.1:0"))
	endpoint, err := s.Endpoint()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Start(context.Background()) }()
	conn := dialTCPTest(t, endpoint.Host)
	defer conn.Close()
	<-h.opened
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("connection remained open after Open panic")
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestDisconnectCleanupDoesNotBlock(t *testing.T) {
	h := &connectionLifecycleHandler{opened: make(chan struct{}, 2)}
	s := newTCPServer(t, h, Address("127.0.0.1:0"))
	endpoint, err := s.Endpoint()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Start(context.Background()) }()
	for range 2 {
		conn := dialTCPTest(t, endpoint.Host)
		<-h.opened
		_ = conn.Close()
	}
	deadline := time.Now().Add(time.Second)
	for h.closed.Load() != 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if h.closed.Load() != 2 {
		t.Fatalf("closed callbacks = %d, want 2", h.closed.Load())
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestOpenFailureDoesNotCallClose(t *testing.T) {
	h := &connectionLifecycleHandler{openErr: errors.New("rejected"), opened: make(chan struct{}, 1)}
	s := newTCPServer(t, h, Address("127.0.0.1:0"))
	endpoint, err := s.Endpoint()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Start(context.Background()) }()
	conn := dialTCPTest(t, endpoint.Host)
	<-h.opened
	_ = conn.Close()
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if h.closed.Load() != 0 {
		t.Fatalf("closed callbacks = %d, want 0", h.closed.Load())
	}
}
