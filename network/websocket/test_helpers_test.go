package websocket

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yola/api/protocol/v1"
	"yola/network"

	"github.com/gorilla/websocket"
)

type websocketTestHandler struct{}

func (websocketTestHandler) Open(context.Context, network.Connection) error { return nil }
func (websocketTestHandler) Handle(_ context.Context, _ network.Connection, message *v1.Proto) (*v1.Proto, error) {
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
func (websocketTestHandler) Close(context.Context, network.Connection) {}

func serverConnectionCount(server *Server) int32 {
	server.lifecycleMu.Lock()
	defer server.lifecycleMu.Unlock()
	return server.connCount
}

func newWebSocketServer(t testing.TB, handler network.ConnectionHandler, opts ...ServerOption) *Server {
	t.Helper()
	server := NewServer(opts...)
	if err := server.SetHandler(handler); err != nil {
		t.Fatalf("Server.SetHandler() error = %v", err)
	}
	return server
}

func dialWebSocketTest(t testing.TB, endpoint string) *websocket.Conn {
	t.Helper()
	conn, response, err := websocket.DefaultDialer.Dial(endpoint, nil)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		t.Fatalf("Dial(%q) error = %v", endpoint, err)
	}
	return conn
}

func startWebSocketTestServer(t testing.TB, handler network.ConnectionHandler, opts ...ServerOption) string {
	t.Helper()
	opts = append([]ServerOption{Address("127.0.0.1:0")}, opts...)
	server := newWebSocketServer(t, handler, opts...)
	return serveWebSocketTestServer(t, server)
}

func serveWebSocketTestServer(t testing.TB, server *Server) string {
	t.Helper()
	endpoint, err := server.Endpoint()
	if err != nil {
		t.Fatalf("Server.Endpoint() error = %v", err)
	}
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Start(context.Background()) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := server.Stop(ctx); err != nil {
			t.Errorf("Stop() error = %v", err)
		}
		if err := waitWebSocketValue(t, serverDone); err != nil {
			t.Errorf("Start() error = %v", err)
		}
	})
	return endpoint.String()
}

func waitWebSocketValue[T any](t testing.TB, values <-chan T) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for channel value")
		var zero T
		return zero
	}
}

type countingCodec struct {
	marshals atomic.Int32
}

func (c *countingCodec) Marshal(any) ([]byte, error) {
	c.marshals.Add(1)
	return []byte{1}, nil
}

func (*countingCodec) Unmarshal([]byte, any) error { return nil }
func (*countingCodec) Name() string                { return defaultCodecName }

type fixedSizeCodec struct {
	size int
}

func (c fixedSizeCodec) Marshal(any) ([]byte, error) { return make([]byte, c.size), nil }
func (fixedSizeCodec) Unmarshal([]byte, any) error   { return nil }
func (fixedSizeCodec) Name() string                  { return "fixed-size" }

var errControlledWrite = errors.New("controlled websocket write failure")

type controlledWriteConn struct {
	net.Conn
	fail    atomic.Bool
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *controlledWriteConn) Write(body []byte) (int, error) {
	if !c.fail.Load() {
		return c.Conn.Write(body)
	}
	c.once.Do(func() { close(c.started) })
	<-c.release
	return 0, errControlledWrite
}

func openTestWebSocket(t *testing.T, clientConn, serverConn net.Conn) *websocket.Conn {
	t.Helper()
	handshakeDone := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(serverConn)
		request, err := http.ReadRequest(reader)
		if err != nil {
			handshakeDone <- err
			return
		}
		_ = request.Body.Close()
		key := request.Header.Get("Sec-WebSocket-Key")
		digest := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		accept := base64.StdEncoding.EncodeToString(digest[:])
		_, err = fmt.Fprintf(serverConn, "HTTP/1.1 101 Switching Protocols\r\n"+
			"Upgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", accept)
		handshakeDone <- err
	}()
	dialer := websocket.Dialer{
		NetDialContext:  func(context.Context, string, string) (net.Conn, error) { return clientConn, nil },
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
	}
	conn, response, err := dialer.DialContext(context.Background(), "ws://example.test/", nil)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		_ = serverConn.Close()
		t.Fatal(err)
	}
	if err = <-handshakeDone; err != nil {
		_ = conn.Close()
		_ = serverConn.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
		_ = serverConn.Close()
	})
	return conn
}
