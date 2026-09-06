package websocket

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"yola/api/protocol/v1"
	"yola/internal/queue"

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

func testInvalidAuthenticationReply(t *testing.T, write func(*websocket.Conn) error, want string, wantErr error) {
	t.Helper()
	written := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := new(websocket.Upgrader).Upgrade(w, r, nil)
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
	_, _, err := c.Request(context.Background(), 1, new(v1.ClientAuthReq))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Request() error = %v, want %v", err, context.DeadlineExceeded)
	}
}

func TestRequestHonorsCanceledContext(t *testing.T) {
	ch := &Channel{ctx: context.Background(), codec: defaultCodec(), outbound: make(chan outboundFrame, 1)}
	c := &Client{requestTimeout: time.Hour, channel: ch}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := c.Request(ctx, 1, new(v1.ClientAuthReq))
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
		body, code, err := c.Request(context.Background(), 1, new(v1.ClientAuthReq))
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
	callbacks := queue.New()
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
