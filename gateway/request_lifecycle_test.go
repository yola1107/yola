package gateway

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	protocolv1 "yola/api/protocol/v1"
	"yola/locate"
	"yola/network/tcp"
	"yola/network/websocket"
	"yola/node"

	"github.com/go-kratos/kratos/v3"
	"github.com/go-kratos/kratos/v3/encoding"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestClientCancellationLeavesStartedRequestRunning(t *testing.T) {
	for _, transportName := range []string{"tcp", "websocket"} {
		for _, interruption := range []string{"cancel", "deadline"} {
			t.Run(transportName+"/"+interruption, func(t *testing.T) {
				store := testLocator(t)
				server, err := node.NewServer(node.Address("127.0.0.1:0"), node.Locator(store))
				require.NoError(t, err)
				started := make(chan context.Context, 1)
				release := make(chan struct{})
				unblock := sync.OnceFunc(func() { close(release) })
				var committed atomic.Int32
				server.RegisterRawHandler(1, func(ctx context.Context, _ []byte) ([]byte, error) {
					started <- ctx
					// 模拟已开始、必须完成的业务；请求取消不能撤回该副作用。
					<-release
					committed.Add(1)
					return []byte("late response"), nil
				})
				server.RegisterRawHandler(2, func(context.Context, []byte) ([]byte, error) {
					return []byte("current response"), nil
				})
				fixture := startRequestLifecycle(t, transportName, store, server, 3*time.Second)
				t.Cleanup(unblock)
				codec := &followingRequestCodec{Codec: encoding.GetCodec("proto"), encoded: make(chan struct{})}
				client := fixture.dial(nil, codec)
				ctx, cancel := context.WithCancel(context.Background())
				wantErr := context.Canceled
				if interruption == "deadline" {
					cancel()
					ctx, cancel = context.WithTimeout(context.Background(), 250*time.Millisecond)
					wantErr = context.DeadlineExceeded
				}
				defer cancel()
				result := make(chan error, 1)
				go func() {
					_, _, requestErr := client.Request(ctx, 1, &emptypb.Empty{})
					result <- requestErr
				}()
				nodeCtx := receiveWithin(t, started)
				if interruption == "cancel" {
					cancel()
				}
				require.ErrorIs(t, receiveWithin(t, result), wantErr)
				require.NoError(t, nodeCtx.Err(), "本地取消没有对应的协议取消帧")
				require.Zero(t, committed.Load())
				following := make(chan lifecycleRequestResult, 1)
				go func() {
					body, code, requestErr := client.Request(t.Context(), 2, &emptypb.Empty{})
					following <- lifecycleRequestResult{body: body, code: code, err: requestErr}
				}()
				// 客户端在创建 pending 后编码帧；此时新请求已等待，旧回复才获准返回。
				receiveWithin(t, codec.encoded)
				unblock()
				reply := receiveWithin(t, following)
				require.NoError(t, reply.err)
				require.Equal(t, int32(codes.OK), reply.code)
				require.Equal(t, "current response", string(reply.body))
				require.Equal(t, int32(1), committed.Load())
			})
		}
	}
}

func TestCanceledForwardRemainsInNodeDrain(t *testing.T) {
	for _, transportName := range []string{"tcp", "websocket"} {
		for _, interruption := range []string{"disconnect", "forward deadline"} {
			t.Run(transportName+"/"+interruption, func(t *testing.T) {
				store := testLocator(t)
				started := make(chan context.Context, 1)
				release := make(chan struct{})
				unblock := sync.OnceFunc(func() { close(release) })
				var committed atomic.Int32
				drained := make(chan int32, 1)
				server, err := node.NewServer(node.Address("127.0.0.1:0"), node.Locator(store),
					node.Drain(func(context.Context) error {
						drained <- committed.Load()
						return nil
					}),
				)
				require.NoError(t, err)
				server.RegisterRawHandler(1, func(ctx context.Context, _ []byte) ([]byte, error) {
					started <- ctx
					<-release
					committed.Add(1)
					return nil, nil
				})
				server.RegisterRawHandler(2, func(context.Context, []byte) ([]byte, error) { return nil, nil })
				rpcTimeout := 3 * time.Second
				if interruption == "forward deadline" {
					rpcTimeout = 250 * time.Millisecond
				}
				fixture := startRequestLifecycle(t, transportName, store, server, rpcTimeout)
				t.Cleanup(unblock)
				client := fixture.dial(nil, nil)
				result := make(chan lifecycleRequestResult, 1)
				go func() {
					_, code, requestErr := client.Request(t.Context(), 1, &emptypb.Empty{})
					result <- lifecycleRequestResult{code: code, err: requestErr}
				}()
				nodeCtx := receiveWithin(t, started)
				if interruption == "disconnect" {
					client.Close()
					require.Error(t, receiveWithin(t, result).err)
					client = fixture.dial(nil, nil)
				} else {
					reply := receiveWithin(t, result)
					require.NoError(t, reply.err)
					require.Equal(t, int32(codes.DeadlineExceeded), reply.code)
				}
				receiveWithin(t, nodeCtx.Done())
				require.Zero(t, committed.Load())
				require.NoError(t, fixture.stopNode())
				// 真实请求被拒绝证明 Stop 已关闭准入，避免用 sleep 猜测停止时序。
				require.Eventually(t, func() bool {
					_, code, requestErr := client.Request(t.Context(), 2, &emptypb.Empty{})
					return requestErr == nil && code == int32(codes.Unavailable)
				}, time.Second, time.Millisecond)
				select {
				case <-drained:
					t.Fatal("Drain ran before the admitted handler returned")
				case <-fixture.nodeDone:
					t.Fatal("App.Run returned before the admitted handler returned")
				default:
				}
				_, err = store.LocateNodeEpoch(t.Context(), "game", "node-a")
				require.NoError(t, err)
				unblock()
				require.Equal(t, int32(1), receiveWithin(t, drained))
				receiveWithin(t, fixture.nodeDone)
				_, err = store.LocateNodeEpoch(t.Context(), "game", "node-a")
				require.ErrorIs(t, err, locate.ErrNodeEpochNotFound)
			})
		}
	}
}

func TestReconnectSurvivesLateConnectionCleanup(t *testing.T) {
	for _, transportName := range []string{"tcp", "websocket"} {
		t.Run(transportName, func(t *testing.T) {
			store := &delayedConnectionCleanup{
				Locator: testLocator(t), started: make(chan locate.GateBinding, 1), release: make(chan struct{}),
			}
			unblock := sync.OnceFunc(func() { close(store.release) })
			server, err := node.NewServer(node.Address("127.0.0.1:0"), node.Locator(store))
			require.NoError(t, err)
			var ownerMu sync.Mutex
			var currentToken string
			sessions := make(chan node.Session, 2)
			disconnected := make(chan string, 2)
			server.RegisterRawHandler(1, func(ctx context.Context, _ []byte) ([]byte, error) {
				sess, _ := node.FromContext(ctx)
				if bindErr := sess.BindNode(ctx); bindErr != nil {
					return nil, bindErr
				}
				ownerMu.Lock()
				currentToken = sess.BindingToken()
				ownerMu.Unlock()
				sessions <- sess
				return nil, nil
			})
			server.OnDisconnect(func(_ context.Context, sess node.Session) error {
				// 连接事件只携带归属凭据；实际业务 owner 决定是否仍属于当前会话。
				ownerMu.Lock()
				if currentToken == sess.BindingToken() {
					currentToken = ""
				}
				ownerMu.Unlock()
				disconnected <- sess.BindingToken()
				return nil
			})
			fixture := startRequestLifecycle(t, transportName, store, server, 3*time.Second)
			t.Cleanup(unblock)
			oldClient := fixture.dial(nil, nil)
			_, code, err := oldClient.Request(t.Context(), 1, &emptypb.Empty{})
			require.NoError(t, err)
			require.Equal(t, int32(codes.OK), code)
			oldSession := receiveWithin(t, sessions)
			oldClient.Close()
			oldBinding := receiveWithin(t, store.started)
			require.Equal(t, oldSession.BindingToken(), oldBinding.BindingToken)
			pushes := make(chan []byte, 1)
			newClient := fixture.dial(pushes, nil)
			_, code, err = newClient.Request(t.Context(), 1, &emptypb.Empty{})
			require.NoError(t, err)
			require.Equal(t, int32(codes.OK), code)
			newSession := receiveWithin(t, sessions)
			require.NotEqual(t, oldSession.BindingToken(), newSession.BindingToken())
			unblock()
			require.Equal(t, oldSession.BindingToken(), receiveWithin(t, disconnected))
			lease, err := store.LocateGate(t.Context(), "game", "player-a")
			require.NoError(t, err)
			require.Equal(t, newSession.BindingToken(), lease.Binding.BindingToken)
			ownerMu.Lock()
			remainingToken := currentToken
			ownerMu.Unlock()
			require.Equal(t, newSession.BindingToken(), remainingToken)
			err = oldSession.Push(t.Context(), 9, &emptypb.Empty{})
			require.Equal(t, codes.NotFound, status.Code(err))
			require.NoError(t, newSession.Push(t.Context(), 9, &emptypb.Empty{}))
			receiveWithin(t, pushes)
			nodeID, err := store.LocateNode(t.Context(), "game", "player-a")
			require.NoError(t, err)
			require.Equal(t, "node-a", nodeID)
		})
	}
}

type lifecycleRequestResult struct {
	body []byte
	code int32
	err  error
}

type lifecycleClient interface {
	Request(context.Context, int32, proto.Message) ([]byte, int32, error)
	Close()
}

type requestLifecycleFixture struct {
	dial     func(chan []byte, encoding.Codec) lifecycleClient
	stopNode func() error
	nodeDone <-chan struct{}
}

func startRequestLifecycle(t *testing.T, transportName string, store locate.Locator, server *node.Server, rpcTimeout time.Duration) requestLifecycleFixture {
	t.Helper()
	appCtx, cancelApp := context.WithCancel(context.Background())
	app := kratos.New(
		kratos.Context(appCtx), kratos.ID("node-a"), kratos.Name("game"), kratos.Metadata(server.Metadata()),
		kratos.BeforeStart(server.BeforeStart), kratos.Server(server), kratos.StopTimeout(3*time.Second),
	)
	done := make(chan struct{})
	var runErr error
	t.Cleanup(func() {
		stopErr := app.Stop()
		cancelApp()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		serverErr := server.Stop(ctx)
		receiveWithin(t, done)
		require.NoError(t, stopErr)
		require.NoError(t, serverErr)
		require.NoError(t, runErr)
	})
	go func() {
		runErr = app.Run()
		close(done)
	}()
	endpoint, err := server.Endpoint()
	require.NoError(t, err)
	probe, err := grpc.NewClient(endpoint.Host, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	defer func() { require.NoError(t, probe.Close()) }()
	probe.Connect()
	readyCtx, readyCancel := context.WithTimeout(t.Context(), time.Second)
	defer readyCancel()
	for state := probe.GetState(); state != connectivity.Ready; state = probe.GetState() {
		require.True(t, probe.WaitForStateChange(readyCtx, state), "Node did not become ready")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	var transport ClientTransport
	if transportName == "tcp" {
		transport = tcp.NewServer(tcp.Listener(listener))
	} else {
		transport = websocket.NewServer(websocket.Listener(listener))
	}
	gate := newTestGateway(t, store, endpoint.String(), Address("127.0.0.1:0"), Transport(transport), RPCTimeout(rpcTimeout))
	startTestApp(t, gate)
	return requestLifecycleFixture{
		stopNode: app.Stop,
		nodeDone: done,
		dial: func(pushes chan []byte, codec encoding.Codec) lifecycleClient {
			t.Helper()
			if codec == nil {
				codec = encoding.GetCodec("proto")
			}
			var client lifecycleClient
			var dialErr error
			handlePush := func(body []byte) {
				if pushes != nil {
					select {
					case pushes <- body:
					case <-t.Context().Done():
					}
				}
			}
			if transportName == "tcp" {
				client, dialErr = tcp.NewClient(t.Context(), tcp.WithAddress(listener.Addr().String()),
					tcp.WithServiceName("game"), tcp.WithToken("synthetic-token"), tcp.WithRequestTimeout(5*time.Second), tcp.WithCodec(codec),
					tcp.WithPushHandler(map[int32]tcp.PushHandler{9: handlePush}))
			} else {
				client, dialErr = websocket.NewClient(t.Context(), websocket.WithEndpoint("ws://"+listener.Addr().String()),
					websocket.WithServiceName("game"), websocket.WithToken("synthetic-token"), websocket.WithRequestTimeout(5*time.Second), websocket.WithCodec(codec),
					websocket.WithPushHandler(map[int32]websocket.PushHandler{9: handlePush}))
			}
			require.NoError(t, dialErr)
			t.Cleanup(client.Close)
			return client
		},
	}
}

type followingRequestCodec struct {
	encoding.Codec
	encoded chan struct{}
	once    sync.Once
}

func (c *followingRequestCodec) Marshal(value any) ([]byte, error) {
	body, err := c.Codec.Marshal(value)
	if message, ok := value.(*protocolv1.Proto); err == nil && ok && message.Op == protocolv1.OpRequest && message.Cmd == 2 {
		c.once.Do(func() { close(c.encoded) })
	}
	return body, err
}

type delayedConnectionCleanup struct {
	locate.Locator
	delayed atomic.Bool
	started chan locate.GateBinding
	release chan struct{}
}

func (l *delayedConnectionCleanup) UnbindGate(ctx context.Context, binding locate.GateBinding) error {
	if !l.delayed.Swap(true) {
		l.started <- binding
		select {
		case <-l.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return l.Locator.UnbindGate(ctx, binding)
}
