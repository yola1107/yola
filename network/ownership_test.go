package network_test

import (
	"bytes"
	"context"
	"net"
	"sync"
	"testing"
	"time"

	v1 "yola/api/protocol/v1"
	"yola/network"
	"yola/network/tcp"
	"yola/network/websocket"

	"github.com/go-kratos/kratos/v3/encoding"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestSendProtoSharesImmutableInputAcrossTransports(t *testing.T) {
	for _, transportName := range []string{"tcp", "websocket"} {
		for _, customCodec := range []bool{false, true} {
			codecName := "default"
			if customCodec {
				codecName = "custom"
			}
			t.Run(transportName+"/"+codecName, func(t *testing.T) {
				conn, received := startOwnershipConnection(t, transportName, customCodec)
				message := &v1.Proto{Op: v1.OpPush, Cmd: 42, Body: bytes.Repeat([]byte{7}, 1024)}
				expected := proto.Clone(message).(*v1.Proto)
				const sends = 16
				results := make(chan error, sends)
				var workers sync.WaitGroup
				workers.Add(sends)
				for range sends {
					go func() {
						defer workers.Done()
						results <- conn.SendProto(message)
					}()
				}
				workers.Wait()
				for range sends {
					require.NoError(t, <-results)
					require.Equal(t, expected.Body, receiveOwnershipPayload(t, received))
				}
				require.True(t, proto.Equal(expected, message), "encoding must not modify shared input")

				prepared := new(network.PreparedProto)
				prepared.Reset(message)
				if capable, ok := conn.(network.PreparedConnection); ok {
					require.NoError(t, capable.SendPrepared(prepared))
				} else {
					require.NoError(t, conn.SendProto(prepared.Message()))
				}
				next := proto.Clone(message).(*v1.Proto)
				next.Body[0] = 9
				prepared.Reset(next)
				require.NoError(t, conn.SendProto(next))
				require.Equal(t, expected.Body, receiveOwnershipPayload(t, received))
				require.Equal(t, next.Body, receiveOwnershipPayload(t, received))
			})
		}
	}
}

type ownershipHandler struct {
	opened chan network.Connection
}

func (h ownershipHandler) Open(_ context.Context, conn network.Connection) error {
	h.opened <- conn
	return nil
}

func (ownershipHandler) Handle(_ context.Context, _ network.Connection, message *v1.Proto) (*v1.Proto, error) {
	switch message.Op {
	case v1.OpAuth:
		message.Op = v1.OpAuthReply
	case v1.OpHeartbeat:
		message.Op = v1.OpHeartbeatReply
	}
	return message, nil
}

func (ownershipHandler) Close(context.Context, network.Connection) {}

func startOwnershipConnection(t *testing.T, transportName string, customCodec bool) (network.Connection, <-chan []byte) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	var server interface {
		SetHandler(network.ConnectionHandler) error
		BeforeStart(context.Context) error
		Start(context.Context) error
		Stop(context.Context) error
	}
	if transportName == "tcp" {
		opts := []tcp.ServerOption{tcp.Listener(listener)}
		if customCodec {
			opts = append(opts, tcp.Codec(encoding.GetCodec("proto")))
		}
		server = tcp.NewServer(opts...)
	} else {
		opts := []websocket.ServerOption{websocket.Listener(listener)}
		if customCodec {
			opts = append(opts, websocket.Codec(encoding.GetCodec("proto")))
		}
		server = websocket.NewServer(opts...)
	}
	opened := make(chan network.Connection, 1)
	require.NoError(t, server.SetHandler(ownershipHandler{opened: opened}))
	require.NoError(t, server.BeforeStart(context.Background()))
	stopped := make(chan error, 1)
	go func() { stopped <- server.Start(context.Background()) }()
	t.Cleanup(func() {
		require.NoError(t, server.Stop(context.Background()))
		require.NoError(t, <-stopped)
	})
	received := make(chan []byte, 32)
	if transportName == "tcp" {
		client, createErr := tcp.NewClient(context.Background(),
			tcp.WithAddress(listener.Addr().String()), tcp.WithServiceName("test"), tcp.WithToken("synthetic-token"),
			tcp.WithPushHandler(map[int32]tcp.PushHandler{42: func(body []byte) { received <- body }}),
		)
		require.NoError(t, createErr)
		t.Cleanup(client.Close)
	} else {
		client, createErr := websocket.NewClient(context.Background(),
			websocket.WithEndpoint("ws://"+listener.Addr().String()),
			websocket.WithServiceName("test"), websocket.WithToken("synthetic-token"),
			websocket.WithPushHandler(map[int32]websocket.PushHandler{42: func(body []byte) { received <- body }}),
		)
		require.NoError(t, createErr)
		t.Cleanup(client.Close)
	}
	select {
	case conn := <-opened:
		return conn, received
	case <-time.After(time.Second):
		t.Fatal("server did not expose its connection")
		return nil, nil
	}
}

func receiveOwnershipPayload(t *testing.T, received <-chan []byte) []byte {
	t.Helper()
	select {
	case body := <-received:
		return body
	case <-time.After(time.Second):
		t.Fatal("immutable message was not delivered")
		return nil
	}
}
