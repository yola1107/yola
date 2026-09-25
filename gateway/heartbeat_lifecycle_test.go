package gateway

import (
	"context"
	"net"
	"net/url"
	"sync"
	"testing"
	"time"

	clusterv1 "yola/api/cluster/v1"
	protocolv1 "yola/api/protocol/v1"
	"yola/locate"
	"yola/network/tcp"
	"yola/network/websocket"

	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestSlowForwardDoesNotBlockHeartbeat(t *testing.T) {
	for _, transportName := range []string{"tcp", "websocket"} {
		t.Run(transportName, func(t *testing.T) {
			checkHeartbeatDuringForward(t, transportName, 50*time.Millisecond, 3*time.Second)
		})
	}
}

func TestDefaultHeartbeatSurvivesExtendedForward(t *testing.T) {
	for _, transportName := range []string{"tcp", "websocket"} {
		t.Run(transportName, func(t *testing.T) {
			interval := 5 * time.Second
			if transportName == "websocket" {
				interval = websocket.DefaultPingInterval
			}
			checkHeartbeatDuringForward(t, transportName, interval, 3*interval)
		})
	}
}

func checkHeartbeatDuringForward(t *testing.T, transportName string, pingInterval, handlerTimeout time.Duration) {
	t.Helper()
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	endpoint := startBackendNode(t, &blockedForwardNode{started: started, release: release})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	grpcListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	heartbeats := make(chan struct{}, 4)
	observeHeartbeat := func(next middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (any, error) {
			reply, handleErr := next(ctx, req)
			if message, ok := reply.(*protocolv1.Proto); handleErr == nil && ok && message.Op == protocolv1.OpHeartbeatReply {
				select {
				case <-started:
					select {
					case heartbeats <- struct{}{}:
					default:
					}
				default:
				}
			}
			return reply, handleErr
		}
	}
	var transport ClientTransport
	if transportName == "tcp" {
		transport = tcp.NewServer(tcp.Listener(listener), tcp.Middleware(observeHeartbeat), tcp.Timeout(handlerTimeout))
	} else {
		transport = websocket.NewServer(websocket.Listener(listener), websocket.Middleware(observeHeartbeat), websocket.Timeout(handlerTimeout))
	}
	gate := newTestGateway(t, testLocator(t), endpoint,
		Listener(grpcListener), Endpoint(&url.URL{Scheme: "grpc", Host: grpcListener.Addr().String()}),
		Transport(transport), RPCTimeout(handlerTimeout),
	)
	startTestApp(t, gate)
	disconnected := make(chan struct{})
	var client interface {
		gatewayRequestClient
		Close()
	}
	if transportName == "tcp" {
		client, err = tcp.NewClient(context.Background(),
			tcp.WithAddress(listener.Addr().String()), tcp.WithServiceName("game"), tcp.WithToken("synthetic-token"),
			tcp.WithPingInterval(pingInterval), tcp.WithDisconnectFunc(func() { close(disconnected) }),
			tcp.WithRequestTimeout(handlerTimeout+time.Second),
		)
	} else {
		client, err = websocket.NewClient(context.Background(),
			websocket.WithEndpoint("ws://"+listener.Addr().String()),
			websocket.WithServiceName("game"), websocket.WithToken("synthetic-token"),
			websocket.WithPingInterval(pingInterval),
			websocket.WithRequestTimeout(handlerTimeout+time.Second),
			websocket.WithDisconnectFunc(func(*websocket.Channel) { close(disconnected) }),
		)
	}
	require.NoError(t, err)
	t.Cleanup(client.Close)
	requestDone := make(chan error, 1)
	go func() {
		_, _, requestErr := client.Request(context.Background(), 1, &emptypb.Empty{})
		requestDone <- requestErr
	}()
	receiveWithin(t, started)
	// 两轮回复证明业务仍阻塞时客户端已确认上一轮心跳。
	for range 2 {
		select {
		case <-heartbeats:
		case <-disconnected:
			t.Fatal("slow Forward caused a heartbeat disconnect")
		case <-time.After(pingInterval + time.Second):
			t.Fatal("heartbeat did not pass the blocked Forward")
		}
	}
	client.Close()
	require.Error(t, receiveWithin(t, requestDone))
}

func TestSessionCloseWaitsForConcurrentHeartbeatRenewal(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	conn := newTestConnection("conn-a")
	sess := activeSession(conn, testBinding())
	sess.leaseDeadline = time.Now().Add(30 * time.Second)
	gate := &Server{
		locator:      &blockedHeartbeatLocator{started: started, release: release},
		leaseTimeout: time.Second,
		leaseTTL:     time.Minute,
		sessions:     &sessionRegistry{byConnID: map[string]*session{"conn-a": sess}},
	}
	renewed := make(chan error, 1)
	go func() { renewed <- gate.Heartbeat(context.Background(), conn) }()
	receiveWithin(t, started)
	detached := make(chan locate.GateBinding, 1)
	go func() { detached <- sess.detachForClose() }()
	// Close 已进入业务锁，必须等续租结束才能撤下 binding。
	require.Eventually(t, func() bool {
		if sess.handlerMu.TryLock() {
			sess.handlerMu.Unlock()
			return false
		}
		return true
	}, time.Second, time.Millisecond)
	select {
	case <-detached:
		t.Fatal("session detached before heartbeat renewal finished")
	default:
	}
	unblock()
	require.NoError(t, receiveWithin(t, renewed))
	require.Equal(t, testBinding(), receiveWithin(t, detached))
	require.Zero(t, sess.binding)
	require.True(t, sess.leaseDeadline.IsZero())
}

type blockedHeartbeatLocator struct {
	locate.Locator
	started chan struct{}
	release <-chan struct{}
}

func (s *blockedHeartbeatLocator) RenewGateLease(_ context.Context, binding locate.GateBinding, ttl time.Duration) (locate.GateLease, error) {
	close(s.started)
	<-s.release
	return locate.GateLease{Binding: binding, TTL: ttl}, nil
}

type blockedForwardNode struct {
	clusterv1.UnimplementedNodeServer
	started chan struct{}
	release <-chan struct{}
}

func (n *blockedForwardNode) Forward(ctx context.Context, _ *clusterv1.ForwardRequest) (*clusterv1.ForwardReply, error) {
	close(n.started)
	select {
	case <-n.release:
		return &clusterv1.ForwardReply{}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
