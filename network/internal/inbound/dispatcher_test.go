package inbound

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yola/api/protocol/v1"
	"yola/network"

	"github.com/stretchr/testify/require"
)

func TestDispatcherPreservesFIFOAndBoundsPendingRequests(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	conn := newTestConnection()
	dispatcher := New(context.Background(), conn, func(ctx context.Context, message *v1.Proto) (*v1.Proto, error) {
		switch message.Op {
		case v1.OpAuth:
			message.Op = v1.OpAuthReply
		case v1.OpHeartbeat:
			message.Op = v1.OpHeartbeatReply
		case v1.OpRequest:
			if message.Seq == 1 {
				close(started)
				select {
				case <-release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			message.Op = v1.OpResponse
		}
		return message, nil
	}, 2)
	t.Cleanup(dispatcher.Stop)
	require.NoError(t, dispatcher.Handle(&v1.Proto{Op: v1.OpAuth}))
	require.Equal(t, v1.OpAuthReply, receive(t, conn.replies).Op)
	require.NoError(t, dispatcher.Handle(&v1.Proto{Op: v1.OpRequest, Seq: 1}))
	receive(t, started)
	for _, seq := range []int32{2, 3} {
		require.NoError(t, dispatcher.Handle(&v1.Proto{Op: v1.OpRequest, Seq: seq}))
	}
	require.ErrorIs(t, dispatcher.Handle(&v1.Proto{Op: v1.OpRequest, Seq: 4}), ErrQueueFull)
	require.NoError(t, dispatcher.Handle(&v1.Proto{Op: v1.OpHeartbeat}))
	require.Equal(t, v1.OpHeartbeatReply, receive(t, conn.replies).Op)
	releaseOnce.Do(func() { close(release) })
	for _, seq := range []int32{1, 2, 3} {
		require.Equal(t, seq, receive(t, conn.replies).Seq)
	}
}

func TestDispatcherStopCancelsWaitsAndDiscardsPending(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	var calls atomic.Int32
	conn := newTestConnection()
	dispatcher := New(context.Background(), conn, func(ctx context.Context, message *v1.Proto) (*v1.Proto, error) {
		if message.Op == v1.OpAuth {
			return &v1.Proto{Op: v1.OpAuthReply}, nil
		}
		calls.Add(1)
		close(started)
		<-ctx.Done()
		close(canceled)
		<-release
		return nil, ctx.Err()
	}, 1)
	t.Cleanup(dispatcher.Stop)
	require.NoError(t, dispatcher.Handle(&v1.Proto{Op: v1.OpAuth}))
	require.NoError(t, dispatcher.Handle(&v1.Proto{Op: v1.OpRequest, Seq: 1}))
	receive(t, started)
	require.NoError(t, dispatcher.Handle(&v1.Proto{Op: v1.OpRequest, Seq: 2}))
	stopped := make(chan struct{})
	go func() {
		dispatcher.Stop()
		close(stopped)
	}()
	receive(t, canceled)
	select {
	case <-stopped:
		t.Fatal("Stop returned while a request still owned the connection")
	default:
	}
	releaseOnce.Do(func() { close(release) })
	receive(t, stopped)
	require.Equal(t, int32(1), calls.Load())
	require.ErrorIs(t, dispatcher.Handle(&v1.Proto{Op: v1.OpRequest}), context.Canceled)
}

func TestDispatcherClosesConnectionOnWorkerFailure(t *testing.T) {
	for _, reason := range []string{"handler error", "handler panic", "send queue full"} {
		t.Run(reason, func(t *testing.T) {
			conn := newTestConnection()
			dispatcher := New(context.Background(), conn, func(_ context.Context, message *v1.Proto) (*v1.Proto, error) {
				if message.Op == v1.OpAuth {
					return &v1.Proto{Op: v1.OpAuthReply}, nil
				}
				switch reason {
				case "handler error":
					return nil, errors.New("request failed")
				case "handler panic":
					panic("request panic")
				default:
					return &v1.Proto{Op: v1.OpResponse}, nil
				}
			}, 1)
			t.Cleanup(dispatcher.Stop)
			require.NoError(t, dispatcher.Handle(&v1.Proto{Op: v1.OpAuth}))
			if reason == "send queue full" {
				conn.sendErr = network.ErrSendQueueFull
			}
			require.NoError(t, dispatcher.Handle(&v1.Proto{Op: v1.OpRequest}))
			receive(t, conn.closed)
			dispatcher.Stop()
		})
	}
}

func BenchmarkAuthenticatedDispatcher(b *testing.B) {
	conn := &testConnection{}
	invoke := func(_ context.Context, message *v1.Proto) (*v1.Proto, error) {
		message.Op = v1.OpAuthReply
		return message, nil
	}
	b.ReportAllocs()
	for b.Loop() {
		dispatcher := New(context.Background(), conn, invoke, network.DefaultRequestQueueSize)
		if err := dispatcher.Handle(&v1.Proto{Op: v1.OpAuth}); err != nil {
			b.Fatal(err)
		}
		dispatcher.Stop()
	}
}

type testConnection struct {
	network.Connection
	replies chan *v1.Proto
	closed  chan struct{}
	once    sync.Once
	sendErr error
}

func newTestConnection() *testConnection {
	return &testConnection{replies: make(chan *v1.Proto, 16), closed: make(chan struct{})}
}

func (*testConnection) ConnID() string { return "test-connection" }

func (c *testConnection) SendProto(message *v1.Proto) error {
	if c.sendErr != nil {
		return c.sendErr
	}
	if c.replies != nil {
		c.replies <- message
	}
	return nil
}

func (c *testConnection) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(time.Second):
		t.Fatal("test synchronization timed out")
		var zero T
		return zero
	}
}
