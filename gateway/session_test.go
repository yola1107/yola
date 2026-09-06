package gateway

import (
	"context"
	"testing"
	"time"

	protocolv1 "yola/api/protocol/v1"
	"yola/locate"

	"github.com/stretchr/testify/require"
)

func TestSessionRoute(t *testing.T) {
	now := time.Now()
	binding := testBinding()
	tests := []struct {
		name     string
		binding  locate.GateBinding
		deadline time.Time
		want     bool
	}{
		{name: "current lease", binding: binding, deadline: now.Add(time.Minute), want: true},
		{name: "expired lease", binding: binding, deadline: now.Add(-time.Millisecond)},
		{name: "missing binding", deadline: now.Add(time.Minute)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sess := &session{
				conn:          newTestConnection(binding.ConnID),
				binding:       test.binding,
				leaseDeadline: test.deadline,
			}

			got, valid := sess.route(now)

			require.Equal(t, test.binding, got)
			require.Equal(t, test.want, valid)
		})
	}
}

func TestSessionHeartbeatRoute(t *testing.T) {
	now := time.Now()
	binding := testBinding()
	const interval = time.Minute
	tests := []struct {
		name     string
		deadline time.Time
		valid    bool
		due      bool
	}{
		{name: "not due", deadline: now.Add(2 * interval), valid: true},
		{name: "due", deadline: now.Add(interval), valid: true, due: true},
		{name: "expired", deadline: now.Add(-time.Millisecond)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sess := &session{
				conn:          newTestConnection(binding.ConnID),
				binding:       binding,
				leaseDeadline: test.deadline,
			}

			got, valid, due := sess.heartbeatRoute(now, interval)

			require.Equal(t, binding, got)
			require.Equal(t, test.valid, valid)
			require.Equal(t, test.due, due)
		})
	}
}

func TestSessionSendIfCurrent(t *testing.T) {
	now := time.Now()
	binding := testBinding()
	stale := binding
	stale.BindingToken = "binding-stale"
	tests := []struct {
		name     string
		expected locate.GateBinding
		deadline time.Time
		matched  bool
	}{
		{name: "current binding", expected: binding, deadline: now.Add(time.Minute), matched: true},
		{name: "stale binding", expected: stale, deadline: now.Add(time.Minute)},
		{name: "expired binding", expected: binding, deadline: now.Add(-time.Millisecond)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			conn := newTestConnection(binding.ConnID)
			sess := &session{conn: conn, binding: binding, leaseDeadline: test.deadline}
			message := &protocolv1.Proto{Op: protocolv1.OpPush}

			matched, err := sess.sendIfCurrent(test.expected, message)

			require.NoError(t, err)
			require.Equal(t, test.matched, matched)
			if test.matched {
				require.Equal(t, message, <-conn.pushes)
				return
			}
			select {
			case <-conn.pushes:
				t.Fatal("stale push entered the connection queue")
			default:
			}
		})
	}
}

func TestSessionDetachForKick(t *testing.T) {
	now := time.Now()
	binding := testBinding()
	stale := binding
	stale.BindingToken = "binding-stale"
	tests := []struct {
		name     string
		expected locate.GateBinding
		deadline time.Time
		detached bool
	}{
		{name: "current binding", expected: binding, deadline: now.Add(time.Minute), detached: true},
		{name: "stale binding", expected: stale, deadline: now.Add(time.Minute)},
		{name: "expired binding", expected: binding, deadline: now.Add(-time.Millisecond)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sess := &session{
				conn:          newTestConnection(binding.ConnID),
				binding:       binding,
				leaseDeadline: test.deadline,
			}

			require.Equal(t, test.detached, sess.detachForKick(test.expected))
			if test.detached {
				require.Empty(t, sess.binding)
				require.True(t, sess.leaseDeadline.IsZero())
				return
			}
			require.Equal(t, binding, sess.binding)
			require.Equal(t, test.deadline, sess.leaseDeadline)
		})
	}
}

func TestSessionDetachForClose(t *testing.T) {
	binding := testBinding()
	tests := []struct {
		name    string
		binding locate.GateBinding
	}{
		{name: "authenticated", binding: binding},
		{name: "unauthenticated"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sess := &session{
				conn:          newTestConnection(binding.ConnID),
				binding:       test.binding,
				leaseDeadline: time.Now().Add(time.Minute),
			}

			require.Equal(t, test.binding, sess.detachForClose())
			require.Empty(t, sess.binding)
			require.True(t, sess.leaseDeadline.IsZero())
		})
	}
}

func TestSessionRegistryCommitAuthentication(t *testing.T) {
	binding := testBinding()
	tests := []struct {
		name      string
		prepare   func(context.Context, *sessionRegistry, *session) context.Context
		boundAt   time.Time
		committed bool
	}{
		{
			name:      "current session",
			prepare:   func(ctx context.Context, _ *sessionRegistry, _ *session) context.Context { return ctx },
			boundAt:   time.Now(),
			committed: true,
		},
		{
			name: "removed session",
			prepare: func(ctx context.Context, registry *sessionRegistry, sess *session) context.Context {
				registry.remove(sess.conn)
				return ctx
			},
			boundAt: time.Now(),
		},
		{
			name: "canceled authentication",
			prepare: func(_ context.Context, _ *sessionRegistry, _ *session) context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			boundAt: time.Now(),
		},
		{
			name:    "expired lease",
			prepare: func(ctx context.Context, _ *sessionRegistry, _ *session) context.Context { return ctx },
			boundAt: time.Now().Add(-2 * time.Minute),
		},
		{
			name: "expired authentication deadline",
			prepare: func(ctx context.Context, _ *sessionRegistry, sess *session) context.Context {
				sess.authDeadline = time.Now().Add(-time.Millisecond)
				return ctx
			},
			boundAt: time.Now(),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := &sessionRegistry{byConnID: make(map[string]*session)}
			conn := newTestConnection(binding.ConnID)
			require.True(t, registry.add(conn, time.Minute))
			sess := registry.get(conn.ConnID())
			require.NotNil(t, sess)
			t.Cleanup(func() { sess.detachForClose() })
			ctx := test.prepare(context.Background(), registry, sess)
			lease := locate.GateLease{Binding: binding, TTL: time.Minute}

			require.Equal(t, test.committed, registry.commitAuthentication(ctx, sess, lease, test.boundAt))
			got, valid := sess.route(time.Now())
			require.Equal(t, test.committed, valid)
			if test.committed {
				require.Equal(t, binding, got)
			} else {
				require.Empty(t, got)
			}
		})
	}
}
