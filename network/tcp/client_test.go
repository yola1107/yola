package tcp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	v1 "yola/api/protocol/v1"
	"yola/network"

	"github.com/go-kratos/kratos/v3/encoding"
)

func TestNewClientCountsConnectAndAuthPushAgainstCallbackCapacity(t *testing.T) {
	endpoint := startTCPTestServer(t, clientAuthHandler{pushBeforeReply: true})
	connected := make(chan struct{}, 1)
	pushed := make(chan struct{}, 1)
	client, err := NewClient(
		context.Background(),
		WithAddress(endpoint),
		WithServiceName("game"),
		WithToken("synthetic-token"),
		WithCallbackQueueSize(1),
		WithConnectFunc(func() { connected <- struct{}{} }),
		WithPushHandler(map[int32]PushHandler{1: func([]byte) { pushed <- struct{}{} }}),
	)
	if err == nil || err.Error() != "tcp: initial callbacks exceed callback queue capacity" {
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

func TestNewClientReportsAuthenticationFailure(t *testing.T) {
	for _, test := range []struct {
		name        string
		handler     clientAuthHandler
		wantErr     error
		timeout     time.Duration
		cancelAfter time.Duration
	}{
		{
			name:    "rejected",
			handler: clientAuthHandler{code: 16},
			wantErr: ErrAuthenticationRejected,
			timeout: time.Second,
		},
		{
			name:    "timeout",
			handler: clientAuthHandler{delay: 50 * time.Millisecond},
			wantErr: context.DeadlineExceeded,
			timeout: 20 * time.Millisecond,
		},
		{
			name:        "canceled",
			handler:     clientAuthHandler{delay: 50 * time.Millisecond},
			wantErr:     context.Canceled,
			timeout:     time.Second,
			cancelAfter: 20 * time.Millisecond,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			endpoint := startTCPTestServer(t, test.handler)
			ctx, cancel := context.WithTimeout(context.Background(), test.timeout)
			defer cancel()
			if test.cancelAfter > 0 {
				timer := time.AfterFunc(test.cancelAfter, cancel)
				defer timer.Stop()
			}
			client, err := NewClient(ctx, WithAddress(endpoint), WithServiceName("game"), WithToken("synthetic-token"))
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("NewClient() error = %v, want %v", err, test.wantErr)
			}
			if client != nil {
				client.Close()
				t.Fatal("NewClient() returned a client after authentication failure")
			}
		})
	}
}

func TestNewClientDeliversPushBeforeAuthenticationReply(t *testing.T) {
	endpoint := startTCPTestServer(t, clientAuthHandler{pushBeforeReply: true})
	pushed := make(chan []byte, 1)
	client, err := NewClient(
		context.Background(),
		WithAddress(endpoint),
		WithServiceName("game"),
		WithToken("synthetic-token"),
		WithPushHandler(map[int32]PushHandler{1: func(body []byte) { pushed <- body }}),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	if body := waitTCPValue(t, pushed); string(body) != "before-auth-reply" {
		t.Fatalf("push body = %q", body)
	}
}

func TestNewClientClosesConnectionWhenContextExpiresAfterAuthentication(t *testing.T) {
	closed := make(chan struct{}, 1)
	endpoint := startTCPTestServer(t, clientAuthHandler{closed: closed})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	connected := make(chan struct{}, 1)
	client, err := NewClient(
		ctx,
		WithAddress(endpoint),
		WithServiceName("game"),
		WithToken("synthetic-token"),
		WithCodec(cancelOnAuthReplyCodec{Codec: defaultCodec(), cancel: cancel}),
		WithConnectFunc(func() { connected <- struct{}{} }),
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("NewClient() error = %v, want %v", err, context.Canceled)
	}
	if client != nil {
		t.Fatalf("NewClient() client = %v, want nil", client)
	}
	select {
	case <-connected:
		t.Fatal("connect callback ran after caller cancellation")
	default:
	}
	waitTCPValue(t, closed)
}

func TestReadAuthenticationReplyRejectsInvalidResponses(t *testing.T) {
	const callbackQueueSize = 1
	writeProto := func(wr *bufio.Writer, message *v1.Proto) error {
		return writeFrame(wr, defaultCodec(), message)
	}
	tests := []struct {
		name  string
		write func(*bufio.Writer) error
		want  string
		is    error
	}{
		{
			name: "malformed frame",
			write: func(wr *bufio.Writer) error {
				_, err := wr.Write([]byte{1, 0, 0, 0, 0xff})
				return err
			},
			is: errInvalidFrame,
		},
		{
			name:  "wrong operation",
			write: func(wr *bufio.Writer) error { return writeProto(wr, &v1.Proto{Op: v1.OpResponse}) },
			want:  "tcp: invalid authentication response",
		},
		{
			name: "push limit",
			write: func(wr *bufio.Writer) error {
				if err := writeProto(wr, &v1.Proto{Op: v1.OpPush}); err != nil {
					return err
				}
				return writeProto(wr, &v1.Proto{Op: v1.OpPush})
			},
			want: "tcp: too many pushes before authentication reply",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			testInvalidAuthenticationReply(t, callbackQueueSize, test.write, test.want, test.is)
		})
	}
}

func TestClientIgnoresNilPushHandler(t *testing.T) {
	client := &Client{pushHandlers: map[int32]PushHandler{1: nil}}
	if !client.handleIncoming(&v1.Proto{Op: v1.OpPush, Cmd: 1}) {
		t.Fatal("client closed after receiving a push with no handler")
	}
}

func TestClientHeartbeatRequiresReplyDespiteOtherTraffic(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := &Client{
		conn:         clientConn,
		codec:        defaultCodec(),
		outbound:     make(chan *v1.Proto, 1),
		done:         make(chan struct{}),
		pingInterval: 10 * time.Millisecond,
		readTimeout:  500 * time.Millisecond,
		writeTimeout: 500 * time.Millisecond,
	}
	t.Cleanup(client.Close)
	t.Cleanup(func() { _ = serverConn.Close() })

	go client.readLoop(bufio.NewReader(clientConn))
	go client.writeLoop(bufio.NewWriter(clientConn))
	go client.heartbeatLoop()
	go func() {
		reader := bufio.NewReader(serverConn)
		writer := bufio.NewWriter(serverConn)
		for {
			message := &v1.Proto{}
			if err := readFrame(reader, defaultCodec(), message); err != nil {
				return
			}
			if err := writeFrame(writer, defaultCodec(), &v1.Proto{Op: v1.OpResponse}); err != nil {
				return
			}
			if err := writer.Flush(); err != nil {
				return
			}
		}
	}()

	select {
	case <-client.done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("client remained connected without a heartbeat reply")
	}
}

func TestClientRequestRejectsOversizedFrameWithoutClosing(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := &Client{
		conn:           clientConn,
		codec:          defaultCodec(),
		outbound:       make(chan *v1.Proto, 1),
		done:           make(chan struct{}),
		requestTimeout: time.Second,
	}
	t.Cleanup(client.Close)
	t.Cleanup(func() { _ = serverConn.Close() })

	_, _, err := client.Request(context.Background(), 1, &v1.Proto{
		Body: make([]byte, v1.MaxProtoSize),
	})
	if !errors.Is(err, network.ErrFrameTooLarge) {
		t.Fatalf("Request() error = %v, want %v", err, network.ErrFrameTooLarge)
	}
	if len(client.outbound) != 0 {
		t.Fatal("oversized request entered the send queue")
	}
	select {
	case <-client.done:
		t.Fatal("oversized request closed the client")
	default:
	}
}

func TestClientReadTimeoutClosesBlackhole(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := &Client{
		conn:        clientConn,
		codec:       defaultCodec(),
		done:        make(chan struct{}),
		readTimeout: 20 * time.Millisecond,
	}
	t.Cleanup(client.Close)
	t.Cleanup(func() { _ = serverConn.Close() })

	started := time.Now()
	go client.readLoop(bufio.NewReader(clientConn))
	select {
	case <-client.done:
	case <-time.After(time.Second):
		t.Fatal("client read remained blocked past its deadline")
	}
	if elapsed := time.Since(started); elapsed < 10*time.Millisecond || elapsed > 200*time.Millisecond {
		t.Fatalf("read loop stopped after %v, want a bounded read near 20ms", elapsed)
	}
}

func TestClientWriteTimeoutClosesBlockedWrite(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := &Client{
		conn:         clientConn,
		codec:        defaultCodec(),
		outbound:     make(chan *v1.Proto, 1),
		done:         make(chan struct{}),
		writeTimeout: 20 * time.Millisecond,
	}
	t.Cleanup(client.Close)
	t.Cleanup(func() { _ = serverConn.Close() })

	started := time.Now()
	go client.writeLoop(bufio.NewWriter(clientConn))
	client.heartbeat.Tick()
	if err := client.send(&v1.Proto{Op: v1.OpHeartbeat}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-client.done:
	case <-time.After(time.Second):
		t.Fatal("client write remained blocked past its deadline")
	}
	if elapsed := time.Since(started); elapsed < 10*time.Millisecond || elapsed > 200*time.Millisecond {
		t.Fatalf("write loop stopped after %v, want a bounded write near 20ms", elapsed)
	}
}

func TestClientRequestReportsTimeout(t *testing.T) {
	h := blockingRequestHandler{started: make(chan struct{}, 1), release: make(chan struct{})}
	release := sync.OnceFunc(func() { close(h.release) })
	defer release()
	endpoint := startTCPTestServer(t, h)
	client, err := NewClient(context.Background(), WithAddress(endpoint), WithServiceName("game"), WithToken("synthetic-token"),
		WithRequestTimeout(20*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	_, _, err = client.Request(context.Background(), 1, &v1.Proto{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Request() error = %v, want %v", err, context.DeadlineExceeded)
	}
	waitTCPValue(t, h.started)
	release()
}

func TestClientRequestHonorsCanceledContext(t *testing.T) {
	c := &Client{requestTimeout: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := c.Request(ctx, 1, &v1.Proto{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Request() error = %v, want %v", err, context.Canceled)
	}
}

func TestClientCloseFailsPendingRequest(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()
	c := &Client{
		conn:           clientConn,
		codec:          defaultCodec(),
		outbound:       make(chan *v1.Proto, 1),
		done:           make(chan struct{}),
		requestTimeout: time.Hour,
	}
	completed := make(chan error, 1)
	go func() {
		_, _, err := c.Request(context.Background(), 1, &v1.Proto{})
		completed <- err
	}()
	if message := <-c.outbound; message.Op != v1.OpRequest {
		t.Fatalf("queued operation = %d, want request", message.Op)
	}
	c.Close()
	if err := <-completed; !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Request() error = %v, want %v", err, net.ErrClosed)
	}
}

func TestClientReceivesFinalKick(t *testing.T) {
	kicked := make(chan *v1.Proto, 1)
	endpoint := startTCPTestServer(t, kickHandler{})
	client, err := NewClient(context.Background(), WithAddress(endpoint), WithServiceName("game"),
		WithToken("synthetic-token"), WithKickHandler(func(code int32) {
			kicked <- &v1.Proto{Code: code}
		}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	_, _, err = client.Request(context.Background(), 1, &v1.ClientAuthReq{})
	if err == nil {
		t.Fatal("Client.Request() succeeded after the final kick")
	}
	got := waitTCPValue(t, kicked)
	if got.Code != 7 {
		t.Fatalf("Kick() code = %d, want 7", got.Code)
	}
}

func TestClientConnectsWithTLS(t *testing.T) {
	certificateServer := httptest.NewTLSServer(http.NotFoundHandler())
	serverTLS := certificateServer.TLS.Clone()
	roots := x509.NewCertPool()
	roots.AddCert(certificateServer.Certificate())
	certificateServer.Close()
	endpoint := startTCPTestServer(t, tcpTestHandler{}, TLSConfig(serverTLS))
	connected := make(chan struct{})
	client, err := NewClient(context.Background(), WithAddress(endpoint), WithServiceName("game"), WithToken("synthetic-token"),
		WithTLSConfig(&tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}), WithConnectFunc(func() { close(connected) }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	waitTCPValue(t, connected)
	reply, code, err := client.Request(context.Background(), 1, &v1.Proto{Op: v1.OpPush})
	if err != nil || code != 0 || len(reply) == 0 {
		t.Fatalf("Request() = %x, %d, %v", reply, code, err)
	}
}

type clientAuthHandler struct {
	code            int32
	delay           time.Duration
	pushBeforeReply bool
	closed          chan<- struct{}
}

func (clientAuthHandler) Open(context.Context, network.Connection) error { return nil }

func (h clientAuthHandler) Handle(_ context.Context, conn network.Connection, message *v1.Proto) (*v1.Proto, error) {
	if h.delay > 0 {
		time.Sleep(h.delay)
	}
	if h.pushBeforeReply {
		if err := conn.SendProto(&v1.Proto{Op: v1.OpPush, Cmd: 1, Body: []byte("before-auth-reply")}); err != nil {
			return nil, err
		}
	}
	message.Op = v1.OpAuthReply
	message.Code = h.code
	return message, nil
}

func (h clientAuthHandler) Close(context.Context, network.Connection) {
	if h.closed != nil {
		select {
		case h.closed <- struct{}{}:
		default:
		}
	}
}

type cancelOnAuthReplyCodec struct {
	encoding.Codec
	cancel context.CancelFunc
}

func (c cancelOnAuthReplyCodec) Unmarshal(data []byte, value any) error {
	if err := c.Codec.Unmarshal(data, value); err != nil {
		return err
	}
	if message, ok := value.(*v1.Proto); ok && message.Op == v1.OpAuthReply {
		c.cancel()
	}
	return nil
}

func testInvalidAuthenticationReply(t *testing.T, callbackQueueSize int, write func(*bufio.Writer) error, want string, wantErr error) {
	t.Helper()
	var wire bytes.Buffer
	wr := bufio.NewWriter(&wire)
	if err := write(wr); err != nil {
		t.Fatal(err)
	}
	if err := wr.Flush(); err != nil {
		t.Fatal(err)
	}

	o := &clientOptions{codec: defaultCodec(), callbackQueueSize: callbackQueueSize}
	_, err := o.readAuthenticationReply(context.Background(), bufio.NewReader(&wire))
	if wantErr != nil {
		if !errors.Is(err, wantErr) {
			t.Fatalf("readAuthenticationReply() error = %v, want %v", err, wantErr)
		}
	} else if err == nil || err.Error() != want {
		t.Fatalf("readAuthenticationReply() error = %v, want %q", err, want)
	}
}

type blockingRequestHandler struct {
	started chan struct{}
	release chan struct{}
}

func (blockingRequestHandler) Open(context.Context, network.Connection) error { return nil }

func (h blockingRequestHandler) Handle(_ context.Context, _ network.Connection, message *v1.Proto) (*v1.Proto, error) {
	if message.Op == v1.OpAuth {
		message.Op = v1.OpAuthReply
		return message, nil
	}
	h.started <- struct{}{}
	<-h.release
	message.Op = v1.OpResponse
	return message, nil
}

func (blockingRequestHandler) Close(context.Context, network.Connection) {}

type kickHandler struct{}

func (kickHandler) Open(context.Context, network.Connection) error { return nil }

func (kickHandler) Handle(ctx context.Context, conn network.Connection, message *v1.Proto) (*v1.Proto, error) {
	if message.Op == v1.OpAuth {
		message.Op = v1.OpAuthReply
		return message, nil
	}
	_ = conn.CloseWithProto(ctx, &v1.Proto{Op: v1.OpKick, Code: 7})
	return message, nil
}

func (kickHandler) Close(context.Context, network.Connection) {}
