package node

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yola/locate"
	locateredis "yola/locate/redis"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestLateBindingWritePreservesReplacement(t *testing.T) {
	for _, backend := range []struct{ name, env string }{
		{name: "miniredis"},
		{name: "standalone", env: "YOLA_REDIS_INTEGRATION"},
		{name: "cluster", env: "YOLA_REDIS_CLUSTER_INTEGRATION"},
	} {
		t.Run(backend.name, func(t *testing.T) {
			var addresses []string
			var password string
			database := 9
			if backend.env == "" {
				addresses = []string{miniredis.RunT(t).Addr()}
			} else {
				address := os.Getenv(backend.env)
				if address == "" {
					t.Skip("set " + backend.env + " to a dedicated disposable instance")
				}
				require.NotEqual(t, "1", address, "an explicit isolated address is required")
				addresses = strings.Split(address, ",")
				password = os.Getenv("YOLA_REDIS_PASSWORD")
			}
			if backend.name == "cluster" {
				database = 0
			}
			newClient := func(barrier *bindingWriteBarrier) redis.UniversalClient {
				options := &redis.UniversalOptions{
					Addrs: addresses, Password: password, DB: database, PoolSize: 1, MaxRetries: -1, ContextTimeoutEnabled: true,
				}
				if barrier != nil {
					options.Dialer = func(ctx context.Context, network, address string) (net.Conn, error) {
						conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
						if err != nil {
							return nil, err
						}
						return &delayedBindingConn{Conn: conn, barrier: barrier}, nil
					}
				}
				if backend.name == "cluster" {
					return redis.NewClusterClient(options.Cluster())
				}
				return redis.NewClient(options.Simple())
			}
			for _, scenario := range []struct{ method, replacement string }{
				{method: "BindNode", replacement: "node-b"},
				{method: "BindNode", replacement: "node-a"},
				{method: "UnbindNode", replacement: "node-a"},
				{method: "UnbindNode", replacement: "node-b"},
			} {
				t.Run(scenario.method+"/"+scenario.replacement, func(t *testing.T) {
					ctx := context.Background()
					service := "i48-" + uuid.NewString()
					tag := "{" + base64.RawURLEncoding.EncodeToString([]byte(service)) + "}:"
					direct := newClient(nil)
					t.Cleanup(func() { require.NoError(t, direct.Close()) })
					store := locateredis.New(direct)
					t.Cleanup(func() {
						cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
						defer cancel()
						keys := []string{
							"locate:node:" + tag + base64.RawURLEncoding.EncodeToString([]byte("player")),
							"locate:node:" + tag + base64.RawURLEncoding.EncodeToString([]byte("warmup")),
							"locate:node:epoch:" + tag + base64.RawURLEncoding.EncodeToString([]byte("node-a")),
							"locate:node:epoch:" + tag + base64.RawURLEncoding.EncodeToString([]byte("node-b")),
						}
						require.NoError(t, direct.Del(cleanupCtx, keys...).Err())
					})
					barrier := &bindingWriteBarrier{tag: []byte(tag), entered: make(chan struct{}), release: make(chan struct{})}
					unblock := sync.OnceFunc(func() { close(barrier.release) })
					oldClient := newClient(barrier)
					t.Cleanup(func() { require.NoError(t, oldClient.Close()) })
					oldStore := &observedBindingLocator{
						Locator: locateredis.New(oldClient), entered: make(chan context.Context, 1), result: make(chan error, 1),
					}
					require.NoError(t, store.RegisterNodeEpoch(ctx, service, "node-a", "old", DefaultNodeEpochTTL))
					// 预热两个脚本及目标 slot 连接，排除 NOSCRIPT 重试在取消后提前结束的假阳性。
					require.NoError(t, oldStore.Locator.BindNode(ctx, service, "warmup", "node-a", "old"))
					require.NoError(t, oldStore.Locator.UnbindNode(ctx, service, "warmup", "node-a", "old"))
					require.NoError(t, store.BindNode(ctx, service, "player", "node-a", "old"))
					server := newTestServer(t, Locator(oldStore))
					publishTestIdentity(server, nodeIdentity{serviceName: service, nodeID: "node-a", epoch: "old"})
					t.Cleanup(func() { require.NoError(t, server.Stop(context.Background())) })
					t.Cleanup(unblock)
					binding := testBinding("player", "conn")
					binding.ServiceName = service
					session := requestSession{binding: binding, server: server}
					barrier.armed.Store(true)
					written := make(chan error, 1)
					go func() { written <- callSessionBinding(ctx, session, scenario.method) }()
					writeCtx := receiveNodeValue(t, oldStore.entered)
					receiveNodeValue(t, barrier.entered)

					// 已通过本地检查并进入真实 socket Write 后，另一连接完成存储代次交接。
					require.NoError(t, store.UnregisterNodeEpoch(ctx, service, "node-a", "old"))
					require.NoError(t, store.RegisterNodeEpoch(ctx, service, "node-a", "new", DefaultNodeEpochTTL))
					replacementEpoch := "new"
					if scenario.replacement == "node-b" {
						replacementEpoch = "other"
						require.NoError(t, store.RegisterNodeEpoch(ctx, service, "node-b", replacementEpoch, DefaultNodeEpochTTL))
					}
					require.NoError(t, store.BindNode(ctx, service, "player", scenario.replacement, replacementEpoch))
					server.lease.Load().cancel(errNodeEpochExpired)
					receiveNodeValue(t, writeCtx.Done())
					unblock()
					require.ErrorIs(t, receiveNodeValue(t, oldStore.result), locate.ErrNodeEpochConflict)
					require.Equal(t, codes.Unavailable, status.Code(receiveNodeValue(t, written)))
					current, err := store.LocateNode(ctx, service, "player")
					require.NoError(t, err)
					require.Equal(t, scenario.replacement, current)
					currentEpoch, err := store.LocateNodeEpoch(ctx, service, "node-a")
					require.NoError(t, err)
					require.Equal(t, "new", currentEpoch)
				})
			}
		})
	}
}

type bindingWriteBarrier struct {
	armed   atomic.Bool
	tag     []byte
	entered chan struct{}
	release chan struct{}
}

type delayedBindingConn struct {
	net.Conn
	barrier *bindingWriteBarrier
}

func (c *delayedBindingConn) Write(payload []byte) (int, error) {
	if bytes.Contains(payload, c.barrier.tag) && c.barrier.armed.CompareAndSwap(true, false) {
		close(c.barrier.entered)
		select {
		case <-c.barrier.release:
		case <-time.After(5 * time.Second):
			return 0, errors.New("timed out waiting to release binding write")
		}
	}
	return c.Conn.Write(payload)
}

type observedBindingLocator struct {
	locate.Locator
	entered chan context.Context
	result  chan error
}

func (l *observedBindingLocator) BindNode(ctx context.Context, service, uid, nodeID, epoch string) error {
	l.entered <- ctx
	err := l.Locator.BindNode(ctx, service, uid, nodeID, epoch)
	l.result <- err
	return err
}

func (l *observedBindingLocator) UnbindNode(ctx context.Context, service, uid, nodeID, epoch string) error {
	l.entered <- ctx
	err := l.Locator.UnbindNode(ctx, service, uid, nodeID, epoch)
	l.result <- err
	return err
}
