package gateway

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	protocolv1 "yola/api/protocol/v1"
	"yola/network"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestBroadcastSendsToEveryAuthenticatedSession(t *testing.T) {
	connections := []*testConnection{
		newTestConnection("conn-a"),
		newTestConnection("conn-b"),
		newTestConnection("conn-c"),
	}
	sessions := &sessionRegistry{byConnID: make(map[string]*session)}
	for _, conn := range connections {
		binding := testBinding()
		binding.UID = conn.ConnID()
		binding.ConnID = conn.ConnID()
		binding.BindingToken = conn.ConnID()
		sessions.byConnID[conn.ConnID()] = activeSession(conn, binding)
	}
	unauthenticated := newTestConnection("conn-pending")
	sessions.byConnID[unauthenticated.ConnID()] = &session{conn: unauthenticated}
	server := &Server{broadcaster: newBroadcaster(sessions, 2, 4)}
	require.NoError(t, server.broadcaster.start())
	t.Cleanup(func() { require.NoError(t, server.broadcaster.stop(context.Background())) })

	payload := []byte("announcement")
	require.NoError(t, server.Broadcast(42, payload))
	payload[0] = 'X'
	for _, conn := range connections {
		select {
		case message := <-conn.pushes:
			require.True(t, proto.Equal(&protocolv1.Proto{
				Op: protocolv1.OpPush, Cmd: 42, Body: []byte("announcement"),
			}, message))
		case <-time.After(time.Second):
			t.Fatalf("broadcast did not reach %s", conn.ConnID())
		}
	}
	select {
	case <-unauthenticated.pushes:
		t.Fatal("broadcast reached an unauthenticated session")
	case <-time.After(20 * time.Millisecond):
	}
}

func TestBroadcastUsesPreparedConnectionWhenAvailable(t *testing.T) {
	conn := &preparedTestConnection{testConnection: newTestConnection("conn-prepared")}
	binding := testBinding()
	binding.ConnID = conn.ConnID()
	sessions := &sessionRegistry{byConnID: map[string]*session{
		conn.ConnID(): activeSession(conn, binding),
	}}
	server := &Server{broadcaster: newBroadcaster(sessions, 1, 1)}
	require.NoError(t, server.broadcaster.start())
	t.Cleanup(func() { require.NoError(t, server.broadcaster.stop(context.Background())) })

	require.NoError(t, server.Broadcast(42, []byte("prepared")))
	select {
	case message := <-conn.pushes:
		require.Equal(t, int32(42), message.Cmd)
	case <-time.After(time.Second):
		t.Fatal("prepared broadcast was not delivered")
	}
	require.Equal(t, int32(1), conn.calls.Load())
}

type preparedTestConnection struct {
	*testConnection
	calls atomic.Int32
}

var _ network.PreparedConnection = (*preparedTestConnection)(nil)

func (c *preparedTestConnection) SendPrepared(message *network.PreparedProto) error {
	c.calls.Add(1)
	return c.SendProto(message.Message())
}

func TestBroadcastQueueIsBounded(t *testing.T) {
	conn := newBlockingConnection("conn-a")
	binding := testBinding()
	sessions := &sessionRegistry{byConnID: map[string]*session{
		conn.ConnID(): {
			conn: conn, binding: binding, leaseDeadline: time.Now().Add(time.Minute),
		},
	}}
	server := &Server{broadcaster: newBroadcaster(sessions, 1, 1)}
	require.NoError(t, server.broadcaster.start())
	t.Cleanup(func() {
		conn.unblock()
		require.NoError(t, server.broadcaster.stop(context.Background()))
	})

	require.NoError(t, server.Broadcast(1, nil))
	select {
	case <-conn.started:
	case <-time.After(time.Second):
		t.Fatal("broadcast worker did not start")
	}
	require.NoError(t, server.Broadcast(2, nil))
	require.ErrorIs(t, server.Broadcast(3, nil), ErrBroadcastQueueFull)
	conn.unblock()

	require.Eventually(t, func() bool {
		conn.mu.Lock()
		defer conn.mu.Unlock()
		return len(conn.commands) == 2
	}, time.Second, time.Millisecond)
	require.Equal(t, []int32{1, 2}, conn.snapshotCommands())
	require.Eventually(t, func() bool {
		return server.BroadcastStats().Completed == 2
	}, time.Second, time.Millisecond)
	stats := server.BroadcastStats()
	require.Equal(t, uint64(2), stats.Accepted)
	require.Equal(t, uint64(2), stats.Completed)
	require.Equal(t, uint64(1), stats.QueueDropped)
	require.Zero(t, stats.SendDropped)
	require.Zero(t, stats.QueueDepth)
	require.Equal(t, 1, stats.QueueCapacity)
}

func TestBroadcastStopTimeoutCanBeWaitedAgain(t *testing.T) {
	conn := newBlockingConnection("conn-a")
	binding := testBinding()
	binding.ConnID = conn.ConnID()
	sessions := &sessionRegistry{byConnID: map[string]*session{
		conn.ConnID(): {
			conn: conn, binding: binding, leaseDeadline: time.Now().Add(time.Minute),
		},
	}}
	broadcaster := newBroadcaster(sessions, 1, 1)
	require.NoError(t, broadcaster.start())
	t.Cleanup(func() {
		conn.unblock()
		require.NoError(t, broadcaster.stop(context.Background()))
	})
	require.NoError(t, broadcaster.enqueue(&protocolv1.Proto{Op: protocolv1.OpPush, Cmd: 1}))
	select {
	case <-conn.started:
	case <-time.After(time.Second):
		t.Fatal("broadcast worker did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	require.ErrorIs(t, broadcaster.stop(ctx), context.DeadlineExceeded)
	cancel()
	require.ErrorIs(t,
		broadcaster.enqueue(&protocolv1.Proto{Op: protocolv1.OpPush, Cmd: 2}),
		ErrBroadcastUnavailable,
	)

	stopped := make(chan error, 1)
	go func() { stopped <- broadcaster.stop(context.Background()) }()
	select {
	case err := <-stopped:
		t.Fatalf("second stop returned before the worker finished: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	conn.unblock()
	select {
	case err := <-stopped:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("second stop did not observe shutdown completion")
	}
}

func TestBroadcastRejectsUnavailableAndOversizedMessages(t *testing.T) {
	server := &Server{broadcaster: newBroadcaster(&sessionRegistry{}, 1, 1)}
	require.ErrorIs(t, server.Broadcast(1, nil), ErrBroadcastUnavailable)
	require.ErrorIs(t, server.Broadcast(1, make([]byte, protocolv1.MaxProtoSize)), network.ErrFrameTooLarge)
}

type blockingConnection struct {
	connID    string
	started   chan struct{}
	release   chan struct{}
	start     sync.Once
	unblocked sync.Once
	mu        sync.Mutex
	commands  []int32
}

func newBlockingConnection(connID string) *blockingConnection {
	return &blockingConnection{
		connID: connID, started: make(chan struct{}), release: make(chan struct{}),
	}
}

func (c *blockingConnection) ConnID() string     { return c.connID }
func (c *blockingConnection) RemoteAddr() string { return "127.0.0.1:5000" }

func (c *blockingConnection) SendProto(message *protocolv1.Proto) error {
	c.start.Do(func() {
		close(c.started)
		<-c.release
	})
	c.mu.Lock()
	c.commands = append(c.commands, message.Cmd)
	c.mu.Unlock()
	return nil
}

func (c *blockingConnection) CloseWithProto(context.Context, *protocolv1.Proto) error {
	return nil
}

func (c *blockingConnection) Close() error { return nil }

func (c *blockingConnection) unblock() {
	c.unblocked.Do(func() { close(c.release) })
}

func (c *blockingConnection) snapshotCommands() []int32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]int32(nil), c.commands...)
}

var _ network.Connection = (*blockingConnection)(nil)
