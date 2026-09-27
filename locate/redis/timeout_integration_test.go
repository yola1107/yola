package redis_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yola/locate"
	locateredis "yola/locate/redis"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestLocatorRedisRequestBudget(t *testing.T) {
	for _, backend := range []string{"miniredis", "standalone"} {
		t.Run(backend, func(t *testing.T) {
			var address, password string
			if backend == "miniredis" {
				address = miniredis.RunT(t).Addr()
			} else {
				address, password = os.Getenv("YOLA_REDIS_INTEGRATION"), os.Getenv("YOLA_REDIS_PASSWORD")
				if address == "" {
					t.Skip("set YOLA_REDIS_INTEGRATION to a disposable Redis instance")
				}
				require.NotEqual(t, "1", address, "an explicit isolated address is required")
			}
			for _, name := range []string{
				"Ping", "BindGate", "LocateGate", "RenewGateLease", "UnbindGate", "BindNode", "LocateNode", "UnbindNode",
				"RegisterNodeEpoch", "RenewNodeEpoch", "LocateNodeEpoch", "UnregisterNodeEpoch",
			} {
				t.Run(name, func(t *testing.T) {
					store, binding, replies := newDelayedRedisLocator(t, address, password)
					replies.armed.Store(true)
					ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
					defer cancel()
					started := time.Now()
					err := locatorOperations(store, binding)[name](ctx)
					require.ErrorIs(t, err, context.DeadlineExceeded)
					var timeout net.Error
					require.ErrorAs(t, err, &timeout, "the original network error must remain available")
					require.True(t, timeout.Timeout())
					require.Less(t, time.Since(started), time.Second, "caller budget must override the 2s socket timeout")
				})
			}
			for _, result := range []struct {
				name  string
				epoch string
				want  error
			}{
				{name: "successful write after cancel", epoch: "epoch"},
				{name: "fencing after cancel", epoch: "stale", want: locate.ErrNodeEpochConflict},
			} {
				t.Run(result.name, func(t *testing.T) {
					store, binding, replies := newDelayedRedisLocator(t, address, password)
					replies.armed.Store(true)
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					done := make(chan error, 1)
					go func() { done <- store.BindNode(ctx, binding.ServiceName, binding.UID, "node", result.epoch) }()
					select {
					case <-replies.entered:
					case <-time.After(time.Second):
						t.Fatal("Redis reply did not reach the barrier")
					}
					cancel()
					select {
					case err := <-done:
						t.Fatalf("in-flight I/O returned before the reply was released: %v", err)
					case <-time.After(20 * time.Millisecond):
					}
					replies.unblock()
					select {
					case err := <-done:
						if result.want == nil {
							require.NoError(t, err)
						} else {
							require.Same(t, result.want, err)
						}
					case <-time.After(time.Second):
						t.Fatal("operation did not finish after releasing the reply")
					}
				})
			}
		})
	}
}

func newDelayedRedisLocator(t *testing.T, address, password string) (locate.Locator, locate.GateBinding, *redisReplyBarrier) {
	t.Helper()
	replies := &redisReplyBarrier{entered: make(chan struct{}), release: make(chan struct{})}
	client := redis.NewClient(&redis.Options{
		Addr: address, Password: password, Dialer: replies.dial, PoolSize: 1, MaxRetries: -1,
		ReadTimeout: 2 * time.Second, WriteTimeout: 2 * time.Second, ContextTimeoutEnabled: true,
	})
	t.Cleanup(func() {
		replies.unblock()
		require.NoError(t, client.Close())
		replies.wait.Wait()
	})
	store := locateredis.New(client)
	binding := newBinding("gate", "conn")
	binding.ServiceName = "deadline-" + rand.Text()
	t.Cleanup(func() {
		replies.unblock()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		tag := base64.RawURLEncoding.EncodeToString([]byte(binding.ServiceName))
		require.NoError(t, client.Del(ctx,
			"locate:gate:{"+base64.RawURLEncoding.EncodeToString([]byte(binding.ServiceName+"\x00"+binding.UID))+"}",
			"locate:node:{"+tag+"}:"+base64.RawURLEncoding.EncodeToString([]byte(binding.UID)),
			"locate:node:epoch:{"+tag+"}:"+base64.RawURLEncoding.EncodeToString([]byte("node")),
			"locate:node:epoch:{"+tag+"}:"+base64.RawURLEncoding.EncodeToString([]byte("new-node")),
		).Err())
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	require.NoError(t, store.RegisterNodeEpoch(ctx, binding.ServiceName, "node", "epoch", time.Minute))
	require.NoError(t, store.BindNode(ctx, binding.ServiceName, binding.UID, "node", "epoch"))
	_, _, err := store.BindGate(ctx, binding, time.Minute)
	require.NoError(t, err)
	return store, binding, replies
}

// redisReplyBarrier 只暂停当前 client 的回包，不暂停 Redis 或影响其他测试连接。
type redisReplyBarrier struct {
	armed   atomic.Bool
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	resume  sync.Once
	wait    sync.WaitGroup
}

func (b *redisReplyBarrier) unblock() { b.resume.Do(func() { close(b.release) }) }

func (b *redisReplyBarrier) dial(ctx context.Context, network, address string) (net.Conn, error) {
	var dialer net.Dialer
	upstream, err := dialer.DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	client, proxy := net.Pipe()
	b.wait.Go(func() {
		defer upstream.Close()
		defer proxy.Close()
		_, _ = io.Copy(upstream, proxy)
	})
	b.wait.Go(func() {
		defer upstream.Close()
		defer proxy.Close()
		_, _ = io.Copy(heldRedisReply{Conn: proxy, barrier: b}, upstream)
	})
	return client, nil
}

type heldRedisReply struct {
	net.Conn
	barrier *redisReplyBarrier
}

func (w heldRedisReply) Write(payload []byte) (int, error) {
	if w.barrier.armed.Load() {
		w.barrier.once.Do(func() { close(w.barrier.entered) })
		<-w.barrier.release
	}
	return w.Conn.Write(payload)
}
