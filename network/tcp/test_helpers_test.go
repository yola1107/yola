package tcp

import (
	"context"
	"net"
	"testing"
	"time"

	"yola/api/protocol/v1"
	"yola/network"
)

type tcpTestHandler struct{}

func (tcpTestHandler) Open(context.Context, network.Connection) error { return nil }
func (tcpTestHandler) Handle(_ context.Context, _ network.Connection, message *v1.Proto) (*v1.Proto, error) {
	switch message.Op {
	case v1.OpAuth:
		message.Op = v1.OpAuthReply
	case v1.OpRequest:
		message.Op = v1.OpResponse
	}
	return message, nil
}
func (tcpTestHandler) Close(context.Context, network.Connection) {}

func newTCPServer(t testing.TB, handler network.ConnectionHandler, opts ...ServerOption) *Server {
	t.Helper()
	server := NewServer(opts...)
	if err := server.SetHandler(handler); err != nil {
		t.Fatalf("Server.SetHandler() error = %v", err)
	}
	return server
}

func dialTCPTest(t testing.TB, address string) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatalf("Dial(%q) error = %v", address, err)
	}
	return conn
}

func startTCPTestServer(t *testing.T, handler network.ConnectionHandler, opts ...ServerOption) string {
	t.Helper()
	opts = append([]ServerOption{Address("127.0.0.1:0")}, opts...)
	server := newTCPServer(t, handler, opts...)
	return serveTCPTestServer(t, server)
}

func serveTCPTestServer(t *testing.T, server *Server) string {
	t.Helper()
	endpoint, err := server.Endpoint()
	if err != nil {
		t.Fatalf("Server.Endpoint() error = %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Start(context.Background()) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := server.Stop(ctx); err != nil {
			t.Errorf("Server.Stop() error = %v", err)
		}
		if err := waitTCPValue(t, done); err != nil {
			t.Errorf("Server.Start() error = %v", err)
		}
	})
	return endpoint.Host
}

func tcpServerConnectionCount(server *Server) int {
	server.lifecycleMu.Lock()
	defer server.lifecycleMu.Unlock()
	return len(server.conns)
}

func waitTCPValue[T any](t *testing.T, values <-chan T) T {
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
