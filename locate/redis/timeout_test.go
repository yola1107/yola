package redis_test

import (
	"context"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"yola/locate"
	locateredis "yola/locate/redis"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

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

// elapsedDeadlineContext 模拟 socket deadline 已到、context 取消尚未发布的窗口。
type elapsedDeadlineContext struct{}

func (elapsedDeadlineContext) Deadline() (time.Time, bool) { return time.Now().Add(-time.Second), true }
func (elapsedDeadlineContext) Done() <-chan struct{}       { return nil }
func (elapsedDeadlineContext) Err() error                  { return nil }
func (elapsedDeadlineContext) Value(any) any               { return nil }

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
