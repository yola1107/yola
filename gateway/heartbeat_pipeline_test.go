package gateway

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	clusterv1 "yola/api/cluster/v1"
	protocolv1 "yola/api/protocol/v1"
	"yola/network/tcp"
	"yola/network/websocket"

	gorilla "github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestPipelinePreservesOrderAndRenewsWhileForwardBlocked(t *testing.T) {
	for _, transportName := range []string{"tcp", "websocket"} {
		t.Run(transportName, func(t *testing.T) {
			backend := newPipelineNode(t)
			gate, peer := newHeartbeatWirePeer(t, transportName, backend, 2)
			require.NoError(t, peer.send(authMessage(t)))
			auth, err := peer.read()
			require.NoError(t, err)
			require.Equal(t, protocolv1.OpAuthReply, auth.Op)
			require.Zero(t, auth.Code)
			require.NoError(t, peer.send(&protocolv1.Proto{Op: protocolv1.OpRequest, Seq: 1, Cmd: 1}))
			require.Equal(t, int32(1), receiveWithin(t, backend.called))
			sessions := gate.sessions.snapshot(nil)
			require.Len(t, sessions, 1)
			sess := sessions[0]
			sess.bindingMu.Lock()
			deadline := time.Now().Add(gate.leaseTTL / 2)
			sess.leaseDeadline = deadline
			sess.bindingMu.Unlock()
			for _, seq := range []int32{2, 3} {
				require.NoError(t, peer.send(&protocolv1.Proto{Op: protocolv1.OpRequest, Seq: seq, Cmd: seq}))
			}
			require.NoError(t, peer.send(&protocolv1.Proto{Op: protocolv1.OpHeartbeat}))
			heartbeat, err := peer.read()
			require.NoError(t, err)
			require.Equal(t, protocolv1.OpHeartbeatReply, heartbeat.Op)
			sess.bindingMu.Lock()
			renewedDeadline := sess.leaseDeadline
			sess.bindingMu.Unlock()
			require.True(t, renewedDeadline.After(deadline), "heartbeat must renew the Gate lease during Forward")

			backend.unblock()
			for _, seq := range []int32{1, 2, 3} {
				reply, readErr := peer.read()
				require.NoError(t, readErr)
				require.Equal(t, protocolv1.OpResponse, reply.Op)
				require.Equal(t, seq, reply.Seq)
				require.Zero(t, reply.Code)
			}
			require.Equal(t, int32(2), receiveWithin(t, backend.called))
			require.Equal(t, int32(3), receiveWithin(t, backend.called))
		})
	}
}

func TestPipelineOverloadClosesAndDiscardsPending(t *testing.T) {
	for _, transportName := range []string{"tcp", "websocket"} {
		t.Run(transportName, func(t *testing.T) {
			backend := newPipelineNode(t)
			gate, peer := newHeartbeatWirePeer(t, transportName, backend, 1)
			require.NoError(t, peer.send(authMessage(t)))
			_, err := peer.read()
			require.NoError(t, err)
			require.NoError(t, peer.send(&protocolv1.Proto{Op: protocolv1.OpRequest, Seq: 1, Cmd: 1}))
			receiveWithin(t, backend.called)
			for _, seq := range []int32{2, 3} {
				require.NoError(t, peer.send(&protocolv1.Proto{Op: protocolv1.OpRequest, Seq: seq, Cmd: seq}))
			}
			reply, err := peer.read()
			if err == nil {
				// 在途 Forward 可以先返回取消响应，排队请求必须丢弃。
				require.Equal(t, protocolv1.OpResponse, reply.Op)
				require.Equal(t, int32(1), reply.Seq)
				require.Equal(t, int32(codes.Canceled), reply.Code)
				_, err = peer.read()
			}
			require.Error(t, err, "overload must close instead of blocking heartbeat reads")
			receiveWithin(t, backend.finished)
			require.Equal(t, int32(1), backend.calls.Load())
			require.Eventually(t, func() bool { return len(gate.sessions.snapshot(nil)) == 0 }, time.Second, time.Millisecond)
		})
	}
}

func TestPipelinePreservesAuthenticationBarrier(t *testing.T) {
	for _, transportName := range []string{"tcp", "websocket"} {
		t.Run(transportName, func(t *testing.T) {
			backend := newPipelineNode(t)
			backend.unblock()
			_, peer := newHeartbeatWirePeer(t, transportName, backend, 2)
			require.NoError(t, peer.send(authMessage(t)))
			require.NoError(t, peer.send(&protocolv1.Proto{Op: protocolv1.OpRequest, Seq: 2, Cmd: 2}))
			for _, operation := range []int32{protocolv1.OpAuthReply, protocolv1.OpResponse} {
				reply, err := peer.read()
				require.NoError(t, err)
				require.Equal(t, operation, reply.Op)
				require.Zero(t, reply.Code)
			}
		})
	}
}

func TestHeartbeatBeforeAuthenticationClosesConnection(t *testing.T) {
	for _, transportName := range []string{"tcp", "websocket"} {
		t.Run(transportName, func(t *testing.T) {
			backend := newPipelineNode(t)
			_, peer := newHeartbeatWirePeer(t, transportName, backend, 2)
			require.NoError(t, peer.send(&protocolv1.Proto{Op: protocolv1.OpHeartbeat}))
			_, err := peer.read()
			require.Error(t, err)
			require.Zero(t, backend.calls.Load())
		})
	}
}

type heartbeatWirePeer struct {
	send func(*protocolv1.Proto) error
	read func() (*protocolv1.Proto, error)
}

func newHeartbeatWirePeer(t *testing.T, transportName string, backend clusterv1.NodeServer, queueSize int) (*Server, heartbeatWirePeer) {
	t.Helper()
	endpoint := startBackendNode(t, backend)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	grpcListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	var transport ClientTransport
	if transportName == "tcp" {
		transport = tcp.NewServer(tcp.Listener(listener), tcp.RequestQueueSize(queueSize))
	} else {
		transport = websocket.NewServer(websocket.Listener(listener), websocket.RequestQueueSize(queueSize))
	}
	gate := newTestGateway(t, testLocator(t), endpoint,
		Listener(grpcListener), Endpoint(&url.URL{Scheme: "grpc", Host: grpcListener.Addr().String()}), Transport(transport),
	)
	startTestApp(t, gate)
	var send func([]byte) error
	var read func() ([]byte, error)
	if transportName == "tcp" {
		conn, dialErr := net.Dial("tcp", listener.Addr().String())
		require.NoError(t, dialErr)
		t.Cleanup(func() { require.NoError(t, conn.Close()) })
		require.NoError(t, conn.SetDeadline(time.Now().Add(3*time.Second)))
		send = func(body []byte) error {
			frame := binary.LittleEndian.AppendUint32(nil, uint32(len(body)))
			frame = append(frame, body...)
			_, writeErr := conn.Write(frame)
			return writeErr
		}
		read = func() ([]byte, error) {
			var header [4]byte
			if _, readErr := io.ReadFull(conn, header[:]); readErr != nil {
				return nil, readErr
			}
			size := binary.LittleEndian.Uint32(header[:])
			require.LessOrEqual(t, size, uint32(protocolv1.MaxProtoSize))
			body := make([]byte, size)
			_, readErr := io.ReadFull(conn, body)
			return body, readErr
		}
	} else {
		conn, response, dialErr := gorilla.DefaultDialer.Dial("ws://"+listener.Addr().String(), nil)
		require.NoError(t, dialErr)
		if response != nil {
			require.NoError(t, response.Body.Close())
		}
		t.Cleanup(func() { require.NoError(t, conn.Close()) })
		require.NoError(t, conn.SetReadDeadline(time.Now().Add(3*time.Second)))
		require.NoError(t, conn.SetWriteDeadline(time.Now().Add(3*time.Second)))
		send = func(body []byte) error { return conn.WriteMessage(gorilla.BinaryMessage, body) }
		read = func() ([]byte, error) {
			_, body, readErr := conn.ReadMessage()
			return body, readErr
		}
	}
	return gate, heartbeatWirePeer{
		send: func(message *protocolv1.Proto) error {
			body, marshalErr := proto.Marshal(message)
			if marshalErr != nil {
				return marshalErr
			}
			return send(body)
		},
		read: func() (*protocolv1.Proto, error) {
			body, readErr := read()
			if readErr != nil {
				return nil, readErr
			}
			message := new(protocolv1.Proto)
			return message, proto.Unmarshal(body, message)
		},
	}
}

type pipelineNode struct {
	clusterv1.UnimplementedNodeServer
	called   chan int32
	finished chan struct{}
	release  chan struct{}
	unblock  func()
	calls    atomic.Int32
}

func newPipelineNode(t *testing.T) *pipelineNode {
	t.Helper()
	node := &pipelineNode{called: make(chan int32, 8), finished: make(chan struct{}, 8), release: make(chan struct{})}
	node.unblock = sync.OnceFunc(func() { close(node.release) })
	t.Cleanup(node.unblock)
	return node
}

func (n *pipelineNode) Forward(ctx context.Context, request *clusterv1.ForwardRequest) (*clusterv1.ForwardReply, error) {
	n.calls.Add(1)
	n.called <- request.Command
	defer func() { n.finished <- struct{}{} }()
	select {
	case <-n.release:
		return &clusterv1.ForwardReply{Body: request.Body}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (*pipelineNode) Disconnect(context.Context, *clusterv1.DisconnectRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}
