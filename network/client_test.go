package network_test

import (
	"context"
	"net"
	"net/url"
	"sync"
	"testing"
	"time"

	v1 "yola/api/protocol/v1"
	"yola/network"
	"yola/network/tcp"
	"yola/network/websocket"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type callbackClient interface {
	Request(context.Context, int32, proto.Message) ([]byte, int32, error)
	Close()
}

func TestClientCallbacksAreOrderedAndDoNotBlockResponses(t *testing.T) {
	for _, transportName := range []string{"tcp", "websocket"} {
		t.Run(transportName, func(t *testing.T) {
			started := make(chan struct{})
			released := make(chan struct{})
			release := sync.OnceFunc(func() { close(released) })
			bodies := make(chan string, 2)
			client, conn, _ := newCallbackClient(t, transportName, 2, func(body []byte) {
				if string(body) == "first" {
					close(started)
					<-released
				}
				bodies <- string(body)
			}, nil, nil)
			t.Cleanup(release)
			require.NoError(t, conn.SendProto(&v1.Proto{Op: v1.OpPush, Cmd: 1, Body: []byte("first")}))
			waitCallbackValue(t, started)
			require.NoError(t, conn.SendProto(&v1.Proto{Op: v1.OpPush, Cmd: 1, Body: []byte("second")}))
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, _, err := client.Request(ctx, 1, &v1.ClientAuthReq{})
			require.NoError(t, err, "response must not wait for the running callback")
			release()
			require.Equal(t, "first", waitCallbackValue(t, bodies))
			require.Equal(t, "second", waitCallbackValue(t, bodies))
		})
	}
}

func TestClientCallbackQueueFullClosesConnection(t *testing.T) {
	for _, transportName := range []string{"tcp", "websocket"} {
		t.Run(transportName, func(t *testing.T) {
			started := make(chan struct{})
			released := make(chan struct{})
			release := sync.OnceFunc(func() { close(released) })
			disconnected := make(chan struct{}, 1)
			start := sync.OnceFunc(func() { close(started) })
			client, conn, closed := newCallbackClient(t, transportName, 1, func([]byte) {
				start()
				<-released
			}, nil, func() { disconnected <- struct{}{} })
			t.Cleanup(release)
			require.NoError(t, conn.SendProto(&v1.Proto{Op: v1.OpPush, Cmd: 1}))
			waitCallbackValue(t, started)
			require.NoError(t, conn.SendProto(&v1.Proto{Op: v1.OpPush, Cmd: 1}))
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, _, err := client.Request(ctx, 1, &v1.ClientAuthReq{})
			require.NoError(t, err, "response must complete while the callback queue is full")
			require.NoError(t, conn.SendProto(&v1.Proto{Op: v1.OpPush, Cmd: 1}))
			waitCallbackValue(t, closed)
			select {
			case <-disconnected:
				t.Fatal("disconnect callback overlapped the running push callback")
			default:
			}
			release()
			waitCallbackValue(t, disconnected)
		})
	}
}

func TestClientKickRunsAfterPushAndBeforeDisconnect(t *testing.T) {
	for _, transportName := range []string{"tcp", "websocket"} {
		t.Run(transportName, func(t *testing.T) {
			started := make(chan struct{})
			released := make(chan struct{})
			release := sync.OnceFunc(func() { close(released) })
			callbacks := make(chan string, 3)
			_, conn, closed := newCallbackClient(t, transportName, 64, func([]byte) {
				close(started)
				<-released
				callbacks <- "push"
			}, func(int32) { callbacks <- "kick" }, func() { callbacks <- "disconnect" })
			t.Cleanup(release)
			require.NoError(t, conn.SendProto(&v1.Proto{Op: v1.OpPush, Cmd: 1}))
			waitCallbackValue(t, started)
			require.NoError(t, conn.SendProto(&v1.Proto{Op: v1.OpKick, Code: 7}))
			waitCallbackValue(t, closed)
			select {
			case callback := <-callbacks:
				t.Fatalf("terminal callback %q overlapped push", callback)
			default:
			}
			release()
			for _, want := range []string{"push", "kick", "disconnect"} {
				require.Equal(t, want, waitCallbackValue(t, callbacks))
			}
		})
	}
}

func TestClientDisconnectCallbackCanCloseClient(t *testing.T) {
	for _, transportName := range []string{"tcp", "websocket"} {
		t.Run(transportName, func(t *testing.T) {
			var client callbackClient
			disconnected := make(chan struct{})
			client, _, _ = newCallbackClient(t, transportName, 64, nil, nil, func() {
				client.Close()
				close(disconnected)
			})
			closed := make(chan struct{})
			go func() {
				client.Close()
				close(closed)
			}()
			waitCallbackValue(t, closed)
			waitCallbackValue(t, disconnected)
		})
	}
}

type callbackTestHandler struct {
	opened chan network.Connection
	closed chan struct{}
}

func (h callbackTestHandler) Open(_ context.Context, conn network.Connection) error {
	h.opened <- conn
	return nil
}

func (callbackTestHandler) Handle(_ context.Context, _ network.Connection, message *v1.Proto) (*v1.Proto, error) {
	switch message.Op {
	case v1.OpAuth:
		message.Op = v1.OpAuthReply
	case v1.OpRequest:
		message.Op = v1.OpResponse
	case v1.OpHeartbeat:
		message.Op = v1.OpHeartbeatReply
	}
	return message, nil
}

func (h callbackTestHandler) Close(context.Context, network.Connection) { close(h.closed) }

func newCallbackClient(
	t *testing.T, transportName string, capacity int,
	push func([]byte), kick func(int32), disconnect func(),
) (callbackClient, network.Connection, <-chan struct{}) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	var server interface {
		SetHandler(network.ConnectionHandler) error
		Endpoint() (*url.URL, error)
		Start(context.Context) error
		Stop(context.Context) error
	}
	if transportName == "tcp" {
		server = tcp.NewServer(tcp.Listener(listener))
	} else {
		server = websocket.NewServer(websocket.Listener(listener))
	}
	handler := callbackTestHandler{opened: make(chan network.Connection, 1), closed: make(chan struct{})}
	require.NoError(t, server.SetHandler(handler))
	endpoint, err := server.Endpoint()
	require.NoError(t, err)
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Start(context.Background()) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if stopErr := server.Stop(ctx); stopErr != nil {
			t.Errorf("stop callback test server: %v", stopErr)
		}
		require.NoError(t, waitCallbackValue(t, serverDone))
	})
	connected := make(chan struct{})
	callbacksDone := make(chan struct{})
	onDisconnect := func() {
		if disconnect != nil {
			disconnect()
		}
		close(callbacksDone)
	}
	var client callbackClient
	if transportName == "tcp" {
		client, err = tcp.NewClient(context.Background(),
			tcp.WithAddress(endpoint.Host), tcp.WithServiceName("game"), tcp.WithToken("synthetic-token"),
			tcp.WithCallbackQueueSize(capacity), tcp.WithConnectFunc(func() { close(connected) }),
			tcp.WithPushHandler(map[int32]tcp.PushHandler{1: push}), tcp.WithKickHandler(kick), tcp.WithDisconnectFunc(onDisconnect),
		)
	} else {
		client, err = websocket.NewClient(context.Background(),
			websocket.WithEndpoint(endpoint.String()), websocket.WithServiceName("game"), websocket.WithToken("synthetic-token"),
			websocket.WithCallbackQueueSize(capacity), websocket.WithConnectFunc(func(*websocket.Channel) { close(connected) }),
			websocket.WithPushHandler(map[int32]websocket.PushHandler{1: push}), websocket.WithKickHandler(kick),
			websocket.WithDisconnectFunc(func(*websocket.Channel) { onDisconnect() }),
		)
	}
	require.NoError(t, err)
	t.Cleanup(func() {
		closed := make(chan struct{})
		go func() {
			client.Close()
			close(closed)
		}()
		waitCallbackValue(t, closed)
		waitCallbackValue(t, callbacksDone)
	})
	waitCallbackValue(t, connected)
	return client, waitCallbackValue(t, handler.opened), handler.closed
}

func waitCallbackValue[T any](t *testing.T, values <-chan T) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for callback contract")
		var zero T
		return zero
	}
}
