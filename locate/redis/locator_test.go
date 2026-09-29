package redis_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
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

const (
	_testTTL = time.Minute
)

func TestNewNilClientReturnsNilLocator(t *testing.T) {
	require.Nil(t, locateredis.New(nil))
	var client *redis.Client
	require.Nil(t, locateredis.New(client))
}

func TestPing(t *testing.T) {
	locator, _ := newLocator(t)
	require.NoError(t, locator.Ping(context.Background()))
}

func TestRejectsInvalidInput(t *testing.T) {
	locator, _ := newLocator(t)
	ctx := context.Background()
	valid := newBinding("gate-a", "conn-a")

	_, _, err := locator.BindGate(ctx, locate.GateBinding{}, _testTTL)
	require.ErrorIs(t, err, locate.ErrInvalidGateBinding)
	_, _, err = locator.BindGate(ctx, valid, time.Nanosecond)
	require.ErrorIs(t, err, locate.ErrInvalidGateTTL)
	_, err = locator.LocateGate(ctx, "", valid.UID)
	require.ErrorIs(t, err, locate.ErrInvalidGateBinding)
	_, err = locator.RenewGateLease(ctx, locate.GateBinding{}, _testTTL)
	require.ErrorIs(t, err, locate.ErrInvalidGateBinding)
	require.ErrorIs(t, locator.UnbindGate(ctx, locate.GateBinding{}), locate.ErrInvalidGateBinding)
	require.ErrorIs(t, locator.BindNode(ctx, "", "", "", "epoch-a"), locate.ErrInvalidNodeBinding)
	require.ErrorIs(t, locator.BindNode(ctx, "game", "player", "node-a", ""), locate.ErrInvalidNodeEpoch)
	_, err = locator.LocateNode(ctx, "", valid.UID)
	require.ErrorIs(t, err, locate.ErrInvalidNodeBinding)
	require.ErrorIs(t, locator.UnbindNode(ctx, "", "", "", "epoch-a"), locate.ErrInvalidNodeBinding)
	require.ErrorIs(t, locator.UnbindNode(ctx, "game", "player", "node-a", ""), locate.ErrInvalidNodeEpoch)
}

func TestLocatorNetworkTimeoutClassification(t *testing.T) {
	expired, expire := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer expire()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	timeout := &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}
	dependencyErr := errors.New("dependency unavailable")
	tests := []struct {
		name string
		ctx  context.Context
		err  error
		want error
	}{
		{name: "caller deadline", ctx: expired, err: timeout, want: context.DeadlineExceeded},
		{name: "deadline before cancellation publication", ctx: elapsedDeadlineContext{}, err: timeout, want: context.DeadlineExceeded},
		{name: "caller cancellation", ctx: canceled, err: timeout, want: context.Canceled},
		{name: "independent network timeout", ctx: context.Background(), err: timeout},
		{name: "fencing after cancellation", ctx: canceled, err: locate.ErrNodeEpochConflict},
		{name: "dependency error after deadline", ctx: expired, err: dependencyErr},
		{name: "existing context error", ctx: expired, err: context.DeadlineExceeded, want: context.DeadlineExceeded},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:0"})
			t.Cleanup(func() { require.NoError(t, client.Close()) })
			client.AddHook(commandErrorHook{err: test.err})
			store := locateredis.New(client)
			for name, operation := range locatorOperations(store, newBinding("gate", "conn")) {
				t.Run(name, func(t *testing.T) {
					err := operation(test.ctx)
					require.ErrorIs(t, err, test.err)
					if test.want != nil {
						require.ErrorIs(t, err, test.want)
					} else {
						require.Same(t, test.err, err)
					}
				})
			}
		})
	}
}

func TestLocatorKeepsSuccessfulResultAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:0"})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	client.AddHook(commandErrorHook{})
	require.NoError(t, locateredis.New(client).Ping(ctx))
}

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
				"Ping", "BindGate", "LocateGate", "RenewGateLease", "UnbindGate", "BindNode", "RenewNode", "LocateNode", "UnbindNode",
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

func TestLocateKeyUsesCanonicalEncoding(t *testing.T) {
	locator, server := newLocator(t)
	binding := newBinding("gate-a", "conn-a")
	_, _, err := locator.BindGate(context.Background(), binding, _testTTL)
	require.NoError(t, err)
	require.NoError(t, locator.RegisterNodeEpoch(context.Background(), binding.ServiceName, "node-a", "epoch-a", _testTTL))
	require.NoError(t, locator.BindNode(context.Background(), binding.ServiceName, binding.UID, "node-a", "epoch-a"))
	require.ElementsMatch(t, []string{
		"locate:gate:{Z2FtZQBzeW50aGV0aWMtcGxheWVy}",
		"locate:node:{Z2FtZQ}:c3ludGhldGljLXBsYXllcg",
		"locate:node:epoch:{Z2FtZQ}:bm9kZS1h",
	}, server.Keys())
}

// elapsedDeadlineContext 模拟 socket deadline 已到、context 取消尚未发布的窗口。
type elapsedDeadlineContext struct{}

func (elapsedDeadlineContext) Deadline() (time.Time, bool) { return time.Now().Add(-time.Second), true }

func (elapsedDeadlineContext) Done() <-chan struct{} { return nil }

func (elapsedDeadlineContext) Err() error { return nil }

func (elapsedDeadlineContext) Value(any) any { return nil }

type commandErrorHook struct{ err error }

func (h commandErrorHook) DialHook(next redis.DialHook) redis.DialHook { return next }

func (h commandErrorHook) ProcessHook(redis.ProcessHook) redis.ProcessHook {
	return func(context.Context, redis.Cmder) error { return h.err }
}

func (h commandErrorHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func locatorOperations(store locate.Locator, binding locate.GateBinding) map[string]func(context.Context) error {
	service, uid := binding.ServiceName, binding.UID
	return map[string]func(context.Context) error{
		"Ping": store.Ping,
		"BindGate": func(ctx context.Context) error {
			_, _, err := store.BindGate(ctx, binding, time.Minute)
			return err
		},
		"LocateGate": func(ctx context.Context) error {
			_, err := store.LocateGate(ctx, service, uid)
			return err
		},
		"RenewGateLease": func(ctx context.Context) error {
			_, err := store.RenewGateLease(ctx, binding, time.Minute)
			return err
		},
		"UnbindGate": func(ctx context.Context) error { return store.UnbindGate(ctx, binding) },
		"BindNode":   func(ctx context.Context) error { return store.BindNode(ctx, service, uid, "node", "epoch") },
		"RenewNode":  func(ctx context.Context) error { return store.RenewNode(ctx, service, uid, "node", "epoch") },
		"LocateNode": func(ctx context.Context) error {
			_, err := store.LocateNode(ctx, service, uid)
			return err
		},
		"UnbindNode": func(ctx context.Context) error { return store.UnbindNode(ctx, service, uid, "node", "epoch") },
		"RegisterNodeEpoch": func(ctx context.Context) error {
			return store.RegisterNodeEpoch(ctx, service, "new-node", "epoch", time.Minute)
		},
		"RenewNodeEpoch": func(ctx context.Context) error {
			return store.RenewNodeEpoch(ctx, service, "node", "epoch", time.Minute)
		},
		"LocateNodeEpoch": func(ctx context.Context) error {
			_, err := store.LocateNodeEpoch(ctx, service, "node")
			return err
		},
		"UnregisterNodeEpoch": func(ctx context.Context) error {
			return store.UnregisterNodeEpoch(ctx, service, "node", "epoch")
		},
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

func newLocator(t *testing.T) (locate.Locator, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	return locateredis.New(client), server
}

func newBinding(gateID, connID string) locate.GateBinding {
	return locate.GateBinding{
		ServiceName:  "game",
		UID:          "synthetic-player",
		GateID:       gateID,
		GateEndpoint: "grpc://" + gateID + ":9000",
		ConnID:       connID,
		BindingToken: "binding-" + connID,
	}
}
