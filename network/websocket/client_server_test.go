package websocket

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yola/api/protocol/v1"
	"yola/network"
	"yola/network/internal/header"

	"github.com/go-kratos/kratos/v3/encoding"
	"github.com/go-kratos/kratos/v3/encoding/protojson"
	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/transport"
)

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
			tr.RequestHeader().Get(header.ConnectionIDKey) == "" || !hasDeadline ||
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

type clientCallbackHandler struct {
	opened chan network.Connection
}

func (h clientCallbackHandler) Open(_ context.Context, conn network.Connection) error {
	h.opened <- conn
	return nil
}

func (clientCallbackHandler) Handle(_ context.Context, _ network.Connection, message *v1.Proto) (*v1.Proto, error) {
	switch message.Op {
	case v1.OpAuth:
		message.Op = v1.OpAuthReply
	case v1.OpRequest:
		message.Op = v1.OpResponse
	case v1.OpHeartbeat:
		message.Op = v1.OpHeartbeatReply
		message.Body = nil
	}
	return message, nil
}

func (clientCallbackHandler) Close(context.Context, network.Connection) {}

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

func TestClientReceivesFinalKick(t *testing.T) {
	handler := new(kickHandler)
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
	_, _, err = client.Request(context.Background(), 1, new(v1.ClientAuthReq))
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

func TestClientCallbacksAreOrderedAndDoNotBlockResponses(t *testing.T) {
	handler := clientCallbackHandler{opened: make(chan network.Connection, 1)}
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
	if _, _, err := client.Request(requestCtx, 1, new(v1.ClientAuthReq)); err != nil {
		t.Fatalf("Request blocked behind callback: %v", err)
	}
	release()
	if first, second := waitWebSocketValue(t, callbackBodies), waitWebSocketValue(t, callbackBodies); first != "first" || second != "second" {
		t.Fatalf("callback order = %q, %q", first, second)
	}
}

func TestClientCallbackQueueFullClosesConnection(t *testing.T) {
	handler := clientCallbackHandler{opened: make(chan network.Connection, 4)}
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
	if _, _, err := client.Request(requestCtx, 1, new(v1.ClientAuthReq)); err != nil {
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
	handler := clientCallbackHandler{opened: make(chan network.Connection, 1)}
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
		_, _, requestErr := client.Request(context.Background(), 1, new(v1.ClientAuthReq))
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

func TestChannelSendConcurrentClose(t *testing.T) {
	endpoint := startWebSocketTestServer(t, websocketTestHandler{})
	client, err := NewClient(context.Background(), WithEndpoint(endpoint), WithServiceName("game"), WithToken("synthetic-token"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	ch := client.Channel()
	start := make(chan struct{})
	errs := make(chan error, 32)
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- ch.SendProto(&v1.Proto{Op: v1.OpHeartbeat})
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		_ = ch.Close()
	}()
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil && !errors.Is(err, network.ErrConnectionClosed) && !errors.Is(err, network.ErrSendQueueFull) {
			t.Fatalf("SendProto() error = %v", err)
		}
	}
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

func (*clientAuthHandler) Close(context.Context, network.Connection) {}

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
