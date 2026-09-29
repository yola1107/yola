package websocket

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yola/api/protocol/v1"
	"yola/internal/queue"
	"yola/network"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
)

func TestReadAuthenticationReplyRejectsInvalidResponses(t *testing.T) {
	writeProto := func(conn *websocket.Conn, message *v1.Proto) error {
		frame, err := marshalFrame(defaultCodec(), message)
		if err != nil {
			return err
		}
		return conn.WriteMessage(websocket.BinaryMessage, frame)
	}
	tests := []struct {
		name  string
		write func(*websocket.Conn) error
		want  string
		is    error
	}{
		{
			name: "non-binary frame",
			write: func(conn *websocket.Conn) error {
				return conn.WriteMessage(websocket.TextMessage, []byte("not binary"))
			},
			want: "websocket: invalid authentication response",
		},
		{
			name: "malformed binary frame",
			write: func(conn *websocket.Conn) error {
				return conn.WriteMessage(websocket.BinaryMessage, []byte{0xff})
			},
			is: errInvalidFrame,
		},
		{
			name: "wrong operation",
			write: func(conn *websocket.Conn) error {
				return writeProto(conn, &v1.Proto{Op: v1.OpResponse})
			},
			want: "websocket: invalid authentication response",
		},
		{
			name: "rejected",
			write: func(conn *websocket.Conn) error {
				return writeProto(conn, &v1.Proto{Op: v1.OpAuthReply, Code: 16})
			},
			is: ErrAuthenticationRejected,
		},
		{
			name: "push limit",
			write: func(conn *websocket.Conn) error {
				if err := writeProto(conn, &v1.Proto{Op: v1.OpPush}); err != nil {
					return err
				}
				return writeProto(conn, &v1.Proto{Op: v1.OpPush})
			},
			want: "websocket: too many pushes before authentication reply",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			testInvalidAuthenticationReply(t, test.write, test.want, test.is)
		})
	}
}

func TestNewClientReportsInitialFailure(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "ws://" + lis.Addr().String()
	_ = lis.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := NewClient(ctx, WithEndpoint(endpoint), WithServiceName("game"), WithToken("synthetic-token"))
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("NewClient succeeded against a closed listener")
		}
	case <-time.After(500 * time.Millisecond):
		cancel()
		t.Fatal("NewClient did not report the initial connection failure")
	}
}

func TestRequestReportsTimeout(t *testing.T) {
	ch := &Channel{ctx: context.Background(), codec: defaultCodec(), outbound: make(chan outboundFrame, 1)}
	c := &Client{
		requestTimeout: 10 * time.Millisecond,
		channel:        ch,
	}
	_, _, err := c.Request(context.Background(), 1, &v1.ClientAuthReq{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Request() error = %v, want %v", err, context.DeadlineExceeded)
	}
}

func TestRequestHonorsCanceledContext(t *testing.T) {
	ch := &Channel{ctx: context.Background(), codec: defaultCodec(), outbound: make(chan outboundFrame, 1)}
	c := &Client{requestTimeout: time.Hour, channel: ch}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := c.Request(ctx, 1, &v1.ClientAuthReq{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Request() error = %v, want %v", err, context.Canceled)
	}
	if len(ch.outbound) != 0 {
		t.Fatal("canceled request was sent")
	}
}

func TestResponseCompletesRequest(t *testing.T) {
	ch := &Channel{ctx: context.Background(), codec: defaultCodec(), outbound: make(chan outboundFrame, 1)}
	c := &Client{
		requestTimeout: time.Hour,
		channel:        ch,
	}
	type requestResult struct {
		body []byte
		code int32
		err  error
	}
	completed := make(chan requestResult, 1)
	go func() {
		body, code, err := c.Request(context.Background(), 1, &v1.ClientAuthReq{})
		completed <- requestResult{body: body, code: code, err: err}
	}()
	var request v1.Proto
	if err := proto.Unmarshal((<-ch.outbound).body, &request); err != nil {
		t.Fatal(err)
	}
	if err := c.dispatchMessage(&v1.Proto{Op: v1.OpResponse, Seq: request.Seq, Body: []byte("reply"), Code: 7}); err != nil {
		t.Fatal(err)
	}
	outcome := <-completed
	if outcome.err != nil || string(outcome.body) != "reply" || outcome.code != 7 {
		t.Fatalf("Request() = %q, %d, %v", outcome.body, outcome.code, outcome.err)
	}
}

func TestClientDisconnectCallbackCanCloseClient(t *testing.T) {
	callbacks := queue.New(64, nil)
	client := &Client{callbacks: callbacks}
	go callbacks.Run()
	done := make(chan struct{})
	client.disconnectFunc = func(*Channel) {
		client.Close()
		close(done)
	}
	client.Close()
	waitWebSocketValue(t, done)
}

func TestClientReceivesFinalKick(t *testing.T) {
	handler := &kickHandler{}
	kicked := make(chan *v1.Proto, 1)
	endpoint := startWebSocketTestServer(t, handler)
	client, err := NewClient(context.Background(), WithEndpoint(endpoint), WithServiceName("game"),
		WithToken("synthetic-token"),
		WithKickHandler(func(code int32) {
			kicked <- &v1.Proto{Code: code}
		}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	_, _, err = client.Request(context.Background(), 1, &v1.ClientAuthReq{})
	if err == nil {
		t.Fatal("Client.Request() succeeded after the final kick")
	}
	got := waitWebSocketValue(t, kicked)
	if got.Code != 7 {
		t.Fatalf("Kick() code = %d, want 7", got.Code)
	}
	time.Sleep(50 * time.Millisecond)
	if got := handler.opened.Load(); got != 1 {
		t.Fatalf("connections after Kick = %d, want 1", got)
	}
}

func TestClientCallbacksAreOrderedAndDoNotBlockResponses(t *testing.T) {
	handler := websocketTestHandler{opened: make(chan network.Connection, 1)}
	endpoint := startWebSocketTestServer(t, handler)
	connected := make(chan struct{})
	callbackStarted := make(chan struct{})
	releaseCallback := make(chan struct{})
	callbackBodies := make(chan string, 2)
	client, err := NewClient(context.Background(), WithEndpoint(endpoint), WithServiceName("game"), WithToken("synthetic-token"),
		WithCallbackQueueSize(2), WithConnectFunc(func(*Channel) { close(connected) }),
		WithPushHandler(map[int32]PushHandler{1: func(body []byte) {
			if string(body) == "first" {
				close(callbackStarted)
				<-releaseCallback
			}
			callbackBodies <- string(body)
		}}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseCallback) }) }
	t.Cleanup(release)
	serverConn := waitWebSocketValue(t, handler.opened)
	waitWebSocketValue(t, connected)
	if err := serverConn.SendProto(&v1.Proto{Op: v1.OpPush, Cmd: 1, Body: []byte("first")}); err != nil {
		t.Fatal(err)
	}
	waitWebSocketValue(t, callbackStarted)
	if err := serverConn.SendProto(&v1.Proto{Op: v1.OpPush, Cmd: 1, Body: []byte("second")}); err != nil {
		t.Fatal(err)
	}
	requestCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, _, err := client.Request(requestCtx, 1, &v1.ClientAuthReq{}); err != nil {
		t.Fatalf("Request blocked behind callback: %v", err)
	}
	release()
	if first, second := waitWebSocketValue(t, callbackBodies), waitWebSocketValue(t, callbackBodies); first != "first" || second != "second" {
		t.Fatalf("callback order = %q, %q", first, second)
	}
}

func TestClientCallbackQueueFullClosesConnection(t *testing.T) {
	handler := websocketTestHandler{opened: make(chan network.Connection, 4)}
	endpoint := startWebSocketTestServer(t, handler)
	connected := make(chan struct{}, 4)
	callbackStarted := make(chan struct{})
	releaseCallback := make(chan struct{})
	disconnected := make(chan struct{}, 4)
	var callbackStartedOnce sync.Once
	client, err := NewClient(context.Background(), WithEndpoint(endpoint), WithServiceName("game"), WithToken("synthetic-token"),
		WithCallbackQueueSize(1), WithConnectFunc(func(*Channel) { connected <- struct{}{} }),
		WithDisconnectFunc(func(*Channel) { disconnected <- struct{}{} }),
		WithPushHandler(map[int32]PushHandler{1: func([]byte) { callbackStartedOnce.Do(func() { close(callbackStarted) }); <-releaseCallback }}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseCallback) }) }
	t.Cleanup(release)
	serverConn := waitWebSocketValue(t, handler.opened)
	waitWebSocketValue(t, connected)
	if err := serverConn.SendProto(&v1.Proto{Op: v1.OpPush, Cmd: 1}); err != nil {
		t.Fatal(err)
	}
	waitWebSocketValue(t, callbackStarted)
	if err := serverConn.SendProto(&v1.Proto{Op: v1.OpPush, Cmd: 1}); err != nil {
		t.Fatal(err)
	}
	requestCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, _, err := client.Request(requestCtx, 1, &v1.ClientAuthReq{}); err != nil {
		t.Fatalf("Request blocked while filling callback queue: %v", err)
	}
	if err := serverConn.SendProto(&v1.Proto{Op: v1.OpPush, Cmd: 1}); err != nil {
		t.Fatal(err)
	}
	waitWebSocketValue(t, client.channel.ctx.Done())
	select {
	case <-disconnected:
		t.Fatal("disconnect callback overlapped the running push callback")
	default:
	}
	release()
	waitWebSocketValue(t, disconnected)
}

func TestClientKickRunsAfterPushAndBeforeDisconnect(t *testing.T) {
	handler := websocketTestHandler{opened: make(chan network.Connection, 1)}
	endpoint := startWebSocketTestServer(t, handler)
	pushStarted := make(chan struct{})
	releasePush := make(chan struct{})
	callbacks := make(chan string, 3)
	client, err := NewClient(
		context.Background(),
		WithEndpoint(endpoint),
		WithServiceName("game"),
		WithToken("synthetic-token"),
		WithPushHandler(map[int32]PushHandler{1: func([]byte) {
			close(pushStarted)
			<-releasePush
			callbacks <- "push"
		}}),
		WithKickHandler(func(int32) { callbacks <- "kick" }),
		WithDisconnectFunc(func(*Channel) { callbacks <- "disconnect" }),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	serverConn := waitWebSocketValue(t, handler.opened)
	if err = serverConn.SendProto(&v1.Proto{Op: v1.OpPush, Cmd: 1}); err != nil {
		t.Fatal(err)
	}
	waitWebSocketValue(t, pushStarted)
	if err = serverConn.SendProto(&v1.Proto{Op: v1.OpKick, Code: 7}); err != nil {
		t.Fatal(err)
	}
	waitWebSocketValue(t, client.channel.ctx.Done())
	select {
	case callback := <-callbacks:
		t.Fatalf("terminal callback %q overlapped push", callback)
	default:
	}
	close(releasePush)
	for _, want := range []string{"push", "kick", "disconnect"} {
		if got := waitWebSocketValue(t, callbacks); got != want {
			t.Fatalf("callback = %q, want %q", got, want)
		}
	}
}

func TestClientCloseClosesConnection(t *testing.T) {
	endpoint := startWebSocketTestServer(t, websocketTestHandler{})
	client, err := NewClient(context.Background(), WithEndpoint(endpoint), WithServiceName("game"), WithToken("synthetic-token"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	writerDone := client.Channel().writerDone
	client.Close()
	waitWebSocketValue(t, writerDone)
	if client.IsAlive() {
		t.Fatal("client remained alive after Close")
	}
}

func TestClientTimesOutWrittenHeartbeatWithoutReply(t *testing.T) {
	received := make(chan struct{}, 1)
	endpoint := startWebSocketTestServer(t, unansweredHeartbeatHandler{received: received})
	client, err := NewClient(
		context.Background(),
		WithEndpoint(endpoint),
		WithServiceName("game"),
		WithToken("synthetic-token"),
		WithPingInterval(10*time.Millisecond),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	waitWebSocketValue(t, received)
	select {
	case <-client.Channel().ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("client remained connected without a reply to a written heartbeat")
	}
}

func TestClientContextCancellationClosesChannel(t *testing.T) {
	handler := blockedRequestHandler{requestStarted: make(chan struct{}, 1)}
	endpoint := startWebSocketTestServer(t, handler)
	ctx, cancel := context.WithCancel(context.Background())
	disconnected := make(chan struct{}, 2)
	client, err := NewClient(ctx, WithEndpoint(endpoint), WithServiceName("game"), WithToken("synthetic-token"),
		WithDisconnectFunc(func(*Channel) { disconnected <- struct{}{} }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	requestDone := make(chan error, 1)
	go func() {
		_, _, requestErr := client.Request(context.Background(), 1, &v1.ClientAuthReq{})
		requestDone <- requestErr
	}()
	waitWebSocketValue(t, handler.requestStarted)
	cancel()
	waitWebSocketValue(t, client.Channel().writerDone)
	if client.IsAlive() {
		t.Fatal("client channel remained open after context cancellation")
	}
	if err := waitWebSocketValue(t, requestDone); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("Request() error = %v, want %v", err, ErrNotConnected)
	}
	waitWebSocketValue(t, disconnected)
	client.Close()
	select {
	case <-disconnected:
		t.Fatal("disconnect callback ran more than once")
	default:
	}
}

func TestNewClientCountsConnectAndAuthPushAgainstCallbackCapacity(t *testing.T) {
	endpoint := startWebSocketTestServer(t, &clientAuthHandler{pushCount: 1})
	connected := make(chan struct{}, 1)
	pushed := make(chan struct{}, 1)
	client, err := NewClient(
		context.Background(),
		WithEndpoint(endpoint),
		WithServiceName("game"),
		WithToken("synthetic-token"),
		WithCallbackQueueSize(1),
		WithConnectFunc(func(*Channel) { connected <- struct{}{} }),
		WithPushHandler(map[int32]PushHandler{1: func([]byte) { pushed <- struct{}{} }}),
	)
	if err == nil || err.Error() != "websocket: initial callbacks exceed callback queue capacity" {
		t.Fatalf("NewClient() error = %v, want callback capacity error", err)
	}
	if client != nil {
		t.Fatal("NewClient() returned a client after callback capacity failure")
	}
	select {
	case <-connected:
		t.Fatal("connect callback ran after initialization failure")
	case <-pushed:
		t.Fatal("push callback ran after initialization failure")
	default:
	}
}

func TestNewClientWaitsForAuthenticationResult(t *testing.T) {
	for _, test := range []struct {
		name        string
		handler     *clientAuthHandler
		timeout     time.Duration
		cancelAfter time.Duration
		wantErr     error
		wantPush    bool
	}{
		{
			name:    "rejected",
			handler: &clientAuthHandler{code: 16},
			timeout: time.Second,
			wantErr: ErrAuthenticationRejected,
		},
		{
			name:    "timeout",
			handler: &clientAuthHandler{delay: 100 * time.Millisecond, code: 16},
			timeout: 30 * time.Millisecond,
			wantErr: context.DeadlineExceeded,
		},
		{
			name:        "canceled",
			handler:     &clientAuthHandler{delay: 100 * time.Millisecond, code: 16},
			timeout:     time.Second,
			cancelAfter: 30 * time.Millisecond,
			wantErr:     context.Canceled,
		},
		{
			name:     "push before reply",
			handler:  &clientAuthHandler{pushBeforeReply: true},
			timeout:  time.Second,
			wantPush: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			testNewClientAuthenticationResult(t, test.handler, test.timeout, test.cancelAfter, test.wantErr, test.wantPush)
		})
	}
}

func testInvalidAuthenticationReply(t *testing.T, write func(*websocket.Conn) error, want string, wantErr error) {
	t.Helper()
	written := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err == nil {
			err = write(conn)
			_ = conn.Close()
		}
		written <- err
	}))
	t.Cleanup(server.Close)
	conn := dialWebSocketTest(t, "ws"+strings.TrimPrefix(server.URL, "http"))
	t.Cleanup(func() { _ = conn.Close() })

	o := &clientOptions{codec: defaultCodec(), callbackQueueSize: 1}
	_, err := o.readAuthenticationReply(context.Background(), conn)
	if wantErr != nil {
		if !errors.Is(err, wantErr) {
			t.Fatalf("readAuthenticationReply() error = %v, want %v", err, wantErr)
		}
	} else if err == nil || err.Error() != want {
		t.Fatalf("readAuthenticationReply() error = %v, want %q", err, want)
	}
	if err := waitWebSocketValue(t, written); err != nil {
		t.Fatalf("write authentication response: %v", err)
	}
}

type unansweredHeartbeatHandler struct {
	websocketTestHandler
	received chan<- struct{}
}

func (h unansweredHeartbeatHandler) Handle(ctx context.Context, conn network.Connection, message *v1.Proto) (*v1.Proto, error) {
	if message.Op == v1.OpHeartbeat {
		select {
		case h.received <- struct{}{}:
		default:
		}
		return &v1.Proto{Op: v1.OpResponse}, nil
	}
	return h.websocketTestHandler.Handle(ctx, conn, message)
}

type kickHandler struct {
	opened atomic.Int32
}

func (h *kickHandler) Open(context.Context, network.Connection) error {
	h.opened.Add(1)
	return nil
}

func (*kickHandler) Handle(ctx context.Context, conn network.Connection, message *v1.Proto) (*v1.Proto, error) {
	switch message.Op {
	case v1.OpAuth:
		message.Op = v1.OpAuthReply
		return message, nil
	case v1.OpHeartbeat:
		return &v1.Proto{Op: v1.OpHeartbeatReply, Seq: message.Seq}, nil
	}
	_ = conn.CloseWithProto(ctx, &v1.Proto{Op: v1.OpKick, Code: 7})
	return message, nil
}

func (*kickHandler) Close(context.Context, network.Connection) {}

type blockedRequestHandler struct {
	websocketTestHandler
	requestStarted chan struct{}
}

func (h blockedRequestHandler) Handle(ctx context.Context, conn network.Connection, message *v1.Proto) (*v1.Proto, error) {
	if message.Op != v1.OpRequest {
		return h.websocketTestHandler.Handle(ctx, conn, message)
	}
	h.requestStarted <- struct{}{}
	<-ctx.Done()
	return nil, ctx.Err()
}

type clientAuthHandler struct {
	opened          atomic.Int32
	delay           time.Duration
	code            int32
	pushBeforeReply bool
	pushCount       int
}

func (h *clientAuthHandler) Open(context.Context, network.Connection) error {
	h.opened.Add(1)
	return nil
}

func (h *clientAuthHandler) Handle(_ context.Context, conn network.Connection, message *v1.Proto) (*v1.Proto, error) {
	if h.delay > 0 {
		time.Sleep(h.delay)
	}
	pushCount := h.pushCount
	if h.pushBeforeReply {
		pushCount = 1
	}
	for range pushCount {
		if err := conn.SendProto(&v1.Proto{Op: v1.OpPush, Cmd: 1, Body: []byte("before-auth-reply")}); err != nil {
			return nil, err
		}
	}
	message.Op = v1.OpAuthReply
	message.Code = h.code
	return message, nil
}

func (*clientAuthHandler) Close(context.Context, network.Connection) {}

func testNewClientAuthenticationResult(
	t *testing.T,
	h *clientAuthHandler,
	timeout time.Duration,
	cancelAfter time.Duration,
	wantErr error,
	wantPush bool,
) {

	t.Helper()
	endpoint := startWebSocketTestServer(t, h)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if cancelAfter > 0 {
		timer := time.AfterFunc(cancelAfter, cancel)
		defer timer.Stop()
	}
	pushed := make(chan []byte, 1)
	client, err := NewClient(ctx, WithEndpoint(endpoint), WithToken("invalid-token"),
		WithServiceName("game"), WithTimeout(timeout),
		WithPushHandler(map[int32]PushHandler{1: func(body []byte) { pushed <- body }}))
	if !errors.Is(err, wantErr) {
		t.Fatalf("NewClient() error = %v, want %v", err, wantErr)
	}
	if (client == nil) != (err != nil) {
		t.Fatalf("NewClient() client = %v, error = %v", client, err)
	}
	if client != nil {
		defer client.Close()
	}
	if got := h.opened.Load(); got != 1 {
		t.Fatalf("authentication connections = %d, want 1", got)
	}
	if wantPush {
		body := waitWebSocketValue(t, pushed)
		if string(body) != "before-auth-reply" {
			t.Fatalf("push body = %q", body)
		}
	}
}
