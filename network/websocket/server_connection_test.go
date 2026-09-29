package websocket

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yola/api/protocol/v1"
	"yola/network"

	"github.com/go-kratos/kratos/v3/encoding"
	"github.com/go-kratos/kratos/v3/encoding/protojson"
	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/transport"
	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
)

func TestServerMessageTimeoutCancelsHandlerContext(t *testing.T) {
	timedOut := make(chan error, 1)
	endpoint := startWebSocketTestServer(t, &messageTimeoutHandler{timedOut: timedOut}, Timeout(20*time.Millisecond))
	client, err := NewClient(
		context.Background(),
		WithEndpoint(endpoint),
		WithServiceName("game"),
		WithToken("synthetic-token"),
	)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	t.Cleanup(func() { client.Close() })

	requestCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, _, err := client.Request(requestCtx, 1, new(v1.ClientAuthReq)); err == nil {
		t.Fatal("Client.Request() error = nil, want connection failure after handler timeout")
	}
	if err := waitWebSocketValue(t, timedOut); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("handler context error = %v, want %v", err, context.DeadlineExceeded)
	}
}

func TestCheckOrigin(t *testing.T) {
	tests := []struct {
		name   string
		s      *Server
		origin string
		want   bool
	}{
		{
			name: "without origin",
			s:    NewServer(),
			want: true,
		},
		{
			name:   "same origin",
			s:      NewServer(),
			origin: "https://game.example",
			want:   true,
		},
		{
			name:   "cross origin",
			s:      NewServer(),
			origin: "https://other.example",
			want:   false,
		},
		{
			name:   "allowed origin",
			s:      NewServer(AllowedOrigins("https://other.example")),
			origin: "https://other.example",
			want:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "https://game.example/ws", nil)
			r.Header.Set("Origin", tt.origin)
			if got := tt.s.checkOrigin(r); got != tt.want {
				t.Fatalf("checkOrigin() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestServerHandshakeDeadlineIncludesOpen(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	finished := make(chan error, 1)
	var releaseOnce sync.Once
	releaseOpen := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseOpen)

	handler := blockingOpenHandler{started: started, release: release, finished: finished}
	endpoint := startWebSocketTestServer(t, handler, HandshakeTimeout(40*time.Millisecond))

	conn := dialWebSocketTest(t, endpoint)
	defer conn.Close()
	<-started
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := conn.ReadMessage(); err == nil {
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

func TestServerLateSuccessfulOpenCallsClose(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	closed := make(chan error, 1)
	var releaseOnce sync.Once
	releaseOpen := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseOpen)

	handler := lateSuccessfulOpenHandler{started: started, release: release, closed: closed}
	endpoint := startWebSocketTestServer(t, handler, HandshakeTimeout(40*time.Millisecond))
	conn := dialWebSocketTest(t, endpoint)
	defer conn.Close()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("Open did not start")
	}
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("connection remained open after the handshake deadline")
	}
	releaseOpen()
	select {
	case err := <-closed:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Close() context error = %v, want %v", err, context.Canceled)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Close was not called after a late successful Open")
	}
}

func TestServerOpenFailureSkipsClose(t *testing.T) {
	h := new(rejectingHandler)
	s := newWebSocketServer(t, h, Address("127.0.0.1:0"))
	endpoint, err := s.Endpoint()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Start(context.Background()) }()
	conn := dialWebSocketTest(t, endpoint.String())
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	_, _, err = conn.ReadMessage()
	if err == nil {
		t.Fatal("ReadMessage() succeeded after handler rejected the connection")
	}
	_ = conn.Close()
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if h.closed.Load() != 0 {
		t.Fatalf("Close called after Open failure: %d", h.closed.Load())
	}
}

func TestServerCloseContextOnClientDisconnect(t *testing.T) {
	h := &closeContextHandler{
		opened: make(chan struct{}),
		closed: make(chan error, 1),
	}
	endpoint := startWebSocketTestServer(t, h)
	conn := dialWebSocketTest(t, endpoint)
	t.Cleanup(func() { _ = conn.Close() })
	waitWebSocketValue(t, h.opened)

	if err := conn.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if got := waitWebSocketValue(t, h.closed); !errors.Is(got, context.Canceled) {
		t.Errorf("ConnectionHandler.Close() context error = %v, want %v", got, context.Canceled)
	}
}

func TestServerCloseContextOnStop(t *testing.T) {
	h := &closeContextHandler{
		opened: make(chan struct{}),
		closed: make(chan error, 1),
	}
	s := newWebSocketServer(t, h, Address("127.0.0.1:0"))
	endpoint, err := s.Endpoint()
	if err != nil {
		t.Fatalf("Server.Endpoint() error = %v", err)
	}
	serverDone := make(chan error, 1)
	go func() { serverDone <- s.Start(context.Background()) }()
	conn := dialWebSocketTest(t, endpoint.String())
	t.Cleanup(func() { _ = conn.Close() })
	waitWebSocketValue(t, h.opened)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = s.Stop(ctx); err != nil {
		t.Errorf("Server.Stop() error = %v, want nil", err)
	}
	if err = waitWebSocketValue(t, serverDone); err != nil {
		t.Errorf("Server.Start() error = %v, want nil", err)
	}
	if got := waitWebSocketValue(t, h.closed); !errors.Is(got, context.Canceled) {
		t.Errorf("ConnectionHandler.Close() context error = %v, want %v", got, context.Canceled)
	}
}

func TestServerRejectsUpgradeCommittedAfterStop(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lis.Close() })
	release := make(chan struct{})
	releaseHandshake := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseHandshake)
	gated := &handshakeWriteListener{
		Listener: lis,
		started:  make(chan struct{}),
		release:  release,
	}
	h := &upgradeLifecycleHandler{opened: make(chan network.Connection, 1)}
	s := newWebSocketServer(t, h, Listener(gated), Address(lis.Addr().String()))
	endpoint, err := s.Endpoint()
	if err != nil {
		t.Fatal(err)
	}
	serverDone := make(chan error, 1)
	go func() { serverDone <- s.Start(context.Background()) }()
	dialDone := make(chan error, 1)
	go func() {
		conn, response, dialErr := websocket.DefaultDialer.Dial(endpoint.String(), nil)
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		if conn != nil {
			_ = conn.Close()
		}
		dialDone <- dialErr
	}()
	waitWebSocketValue(t, gated.started)

	stopCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = s.Stop(stopCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Server.Stop() error = %v, want %v", err, context.Canceled)
	}
	releaseHandshake()
	_ = waitWebSocketValue(t, dialDone)
	if err = waitWebSocketValue(t, serverDone); err != nil {
		t.Fatalf("Server.Start() error = %v", err)
	}
	select {
	case <-h.opened:
		t.Fatal("ConnectionHandler.Open was called after Stop won the upgrade commit")
	default:
	}
	if count, channels := serverConnectionCount(s), serverChannelCount(s); count != 0 || channels != 0 {
		t.Fatalf("connection state after rejected commit = %d/%d, want 0/0", count, channels)
	}
}

func TestServerServeFailureClosesActiveChannel(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h := &closeContextHandler{
		opened: make(chan struct{}),
		closed: make(chan error, 1),
	}
	s := newWebSocketServer(t, h, Listener(lis), Address(lis.Addr().String()))
	endpoint, err := s.Endpoint()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Start(context.Background()) }()
	conn := dialWebSocketTest(t, endpoint.String())
	t.Cleanup(func() {
		_ = conn.Close()
		_ = s.Stop(context.Background())
	})
	waitWebSocketValue(t, h.opened)

	if err = lis.Close(); err != nil {
		t.Fatal(err)
	}
	if err = waitWebSocketValue(t, done); err == nil {
		t.Fatal("Server.Start() error = nil, want listener failure")
	}
	if got := waitWebSocketValue(t, h.closed); !errors.Is(got, context.Canceled) {
		t.Errorf("ConnectionHandler.Close() context error = %v, want %v", got, context.Canceled)
	}
	if count := serverConnectionCount(s); count != 0 {
		t.Fatalf("connection count after Serve failure = %d, want 0", count)
	}
	channelCount := serverChannelCount(s)
	if channelCount != 0 {
		t.Fatalf("active channels after Serve failure = %d, want 0", channelCount)
	}
}

func TestServerRejectsOversizedProto(t *testing.T) {
	h := new(countingHandler)
	s := newWebSocketServer(t, h, Address("127.0.0.1:0"))
	endpoint, err := s.Endpoint()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Start(context.Background()) }()
	conn := dialWebSocketTest(t, endpoint.String())
	body, err := proto.Marshal(&v1.Proto{Op: v1.OpRequest, Body: make([]byte, v1.MaxProtoSize)})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteMessage(websocket.BinaryMessage, body); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("oversized websocket message was accepted")
	}
	if h.handled.Load() != 0 {
		t.Fatalf("handled oversized messages = %d, want 0", h.handled.Load())
	}
	_ = conn.Close()
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestServerAcceptsFragmentedProto(t *testing.T) {
	endpoint := startWebSocketTestServer(t, websocketTestHandler{})
	dialer := websocket.Dialer{ReadBufferSize: 64, WriteBufferSize: 64}
	conn, response, err := dialer.Dial(endpoint, nil)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	want := &v1.Proto{Op: v1.OpRequest, Cmd: 23, Body: make([]byte, 2048)}
	for index := range want.Body {
		want.Body[index] = byte(index)
	}
	frame, err := proto.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := conn.NextWriter(websocket.BinaryMessage)
	if err != nil {
		t.Fatal(err)
	}
	for len(frame) > 0 {
		chunk := min(17, len(frame))
		if _, err = writer.Write(frame[:chunk]); err != nil {
			t.Fatal(err)
		}
		frame = frame[chunk:]
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}

	messageType, body, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if messageType != websocket.BinaryMessage {
		t.Fatalf("message type = %d, want %d", messageType, websocket.BinaryMessage)
	}
	got := new(v1.Proto)
	if err = proto.Unmarshal(body, got); err != nil {
		t.Fatal(err)
	}
	want.Op = v1.OpResponse
	if !proto.Equal(got, want) {
		t.Fatalf("response = %v, want %v", got, want)
	}
}

func TestServerMessageContextAndReplacementReply(t *testing.T) {
	observed := make(chan error, 1)
	handler := &messageContextHandler{observed: observed}
	codec := encoding.GetCodec(protojson.Name)
	endpoint := startWebSocketTestServer(t, handler, Codec(codec), Timeout(time.Second), Middleware(
		func(next middleware.Handler) middleware.Handler {
			return func(ctx context.Context, request any) (any, error) {
				return next(context.WithValue(ctx, messageContextKey{}, true), request)
			}
		},
	))
	handler.endpoint = endpoint
	client, err := NewClient(
		context.Background(),
		WithEndpoint(endpoint),
		WithServiceName("game"),
		WithToken("synthetic-token"),
		WithCodec(codec),
	)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	t.Cleanup(func() { client.Close() })

	body, code, err := client.Request(context.Background(), 7, new(v1.ClientAuthReq))
	if err != nil {
		t.Fatalf("Client.Request() error = %v", err)
	}
	if string(body) != "replacement" || code != 23 {
		t.Errorf("Client.Request() = (%q, %d), want (%q, %d)", body, code, "replacement", 23)
	}
	if err := waitWebSocketValue(t, observed); err != nil {
		t.Errorf("handler message context error = %v", err)
	}
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

type lateSuccessfulOpenHandler struct {
	started chan struct{}
	release <-chan struct{}
	closed  chan<- error
}

func (h lateSuccessfulOpenHandler) Open(context.Context, network.Connection) error {
	close(h.started)
	<-h.release
	return nil
}

func (lateSuccessfulOpenHandler) Handle(context.Context, network.Connection, *v1.Proto) (*v1.Proto, error) {
	return nil, nil
}

func (h lateSuccessfulOpenHandler) Close(ctx context.Context, _ network.Connection) {
	h.closed <- ctx.Err()
}

type blockingOpenHandler struct {
	started  chan struct{}
	release  chan struct{}
	finished chan error
}

func (h blockingOpenHandler) Open(ctx context.Context, _ network.Connection) error {
	h.started <- struct{}{}
	select {
	case <-h.release:
		return nil
	case <-ctx.Done():
		h.finished <- ctx.Err()
		return ctx.Err()
	}
}

func (blockingOpenHandler) Handle(context.Context, network.Connection, *v1.Proto) (*v1.Proto, error) {
	return nil, nil
}

func (blockingOpenHandler) Close(context.Context, network.Connection) {}

type countingHandler struct{ handled atomic.Int32 }

func (*countingHandler) Open(context.Context, network.Connection) error { return nil }

func (h *countingHandler) Handle(_ context.Context, _ network.Connection, message *v1.Proto) (*v1.Proto, error) {
	h.handled.Add(1)
	return message, nil
}

func (*countingHandler) Close(context.Context, network.Connection) {}

type rejectingHandler struct{ closed atomic.Int32 }

func (*rejectingHandler) Open(context.Context, network.Connection) error {
	return errors.New("connection rejected")
}

func (*rejectingHandler) Handle(_ context.Context, _ network.Connection, message *v1.Proto) (*v1.Proto, error) {
	return message, nil
}

func (h *rejectingHandler) Close(context.Context, network.Connection) { h.closed.Add(1) }

type closeContextHandler struct {
	opened chan struct{}
	closed chan error
}

func (h *closeContextHandler) Open(context.Context, network.Connection) error {
	close(h.opened)
	return nil
}

func (*closeContextHandler) Handle(_ context.Context, _ network.Connection, message *v1.Proto) (*v1.Proto, error) {
	return message, nil
}

func (h *closeContextHandler) Close(ctx context.Context, _ network.Connection) {
	h.closed <- ctx.Err()
}

type upgradeLifecycleHandler struct {
	opened chan network.Connection
}

func (h *upgradeLifecycleHandler) Open(_ context.Context, conn network.Connection) error {
	h.opened <- conn
	return nil
}

func (*upgradeLifecycleHandler) Handle(_ context.Context, _ network.Connection, message *v1.Proto) (*v1.Proto, error) {
	return message, nil
}

func (*upgradeLifecycleHandler) Close(context.Context, network.Connection) {}

type handshakeWriteListener struct {
	net.Listener
	started chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (l *handshakeWriteListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &handshakeWriteConn{Conn: conn, gate: l}, nil
}

type handshakeWriteConn struct {
	net.Conn
	gate *handshakeWriteListener
}

func (c *handshakeWriteConn) Write(body []byte) (int, error) {
	c.gate.once.Do(func() {
		close(c.gate.started)
		<-c.gate.release
	})
	return c.Conn.Write(body)
}

func serverChannelCount(server *Server) int {
	server.lifecycleMu.Lock()
	defer server.lifecycleMu.Unlock()
	return len(server.channels)
}

type messageContextKey struct{}

type messageContextHandler struct {
	observed chan<- error
	endpoint string
}

func (*messageContextHandler) Open(context.Context, network.Connection) error { return nil }

func (h *messageContextHandler) Handle(ctx context.Context, _ network.Connection, message *v1.Proto) (*v1.Proto, error) {
	if message.Op == v1.OpAuth {
		return &v1.Proto{Op: v1.OpAuthReply, Seq: message.Seq}, nil
	}
	tr, ok := transport.FromServerContext(ctx)
	if !ok {
		h.observed <- errors.New("transport context is unavailable")
	} else {
		_, hasDeadline := ctx.Deadline()
		if tr.Operation() != network.ConnectionHandlerOperation || tr.Endpoint() != h.endpoint ||
			tr.RequestHeader().Get("remote_ip") != "127.0.0.1" ||
			tr.RequestHeader().Get("conn_id") == "" || !hasDeadline ||
			ctx.Value(messageContextKey{}) != true {
			h.observed <- fmt.Errorf("unexpected message context: transport=%+v deadline=%t middleware=%v",
				tr, hasDeadline, ctx.Value(messageContextKey{}))
		} else {
			h.observed <- nil
		}
	}
	return &v1.Proto{
		Op:   v1.OpResponse,
		Seq:  message.Seq,
		Cmd:  message.Cmd,
		Code: 23,
		Body: []byte("replacement"),
	}, nil
}

func (*messageContextHandler) Close(context.Context, network.Connection) {}
