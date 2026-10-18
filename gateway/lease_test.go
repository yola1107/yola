package gateway

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yola/locate"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var errRenewalUnavailable = errors.New("redis unavailable")

func BenchmarkGateLeaseRenewalFailureWave(b *testing.B) {
	for _, connectionCount := range []int{1000, 10000} {
		b.Run(fmt.Sprintf("connections=%d/delay=10ms", connectionCount), func(b *testing.B) {
			benchmarkRenewalWave(b, connectionCount, 10*time.Millisecond, time.Second)
		})
	}
	b.Run("connections=10000/timeout=100ms", func(b *testing.B) {
		benchmarkRenewalWave(b, 10000, time.Second, 100*time.Millisecond)
	})
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

func TestLocateStatusCode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want codes.Code
	}{
		{name: "gate missing", err: locate.ErrGateNotFound, want: codes.Aborted},
		{name: "gate conflict", err: locate.ErrGateConflict, want: codes.Aborted},
		{name: "canceled", err: context.Canceled, want: codes.Canceled},
		{name: "deadline", err: context.DeadlineExceeded, want: codes.DeadlineExceeded},
		{name: "invalid binding", err: locate.ErrInvalidGateBinding, want: codes.Internal},
		{name: "invalid lease", err: locate.ErrInvalidGateLease, want: codes.Internal},
		{name: "invalid ttl", err: locate.ErrInvalidGateTTL, want: codes.Internal},
		{name: "redis failure", err: errors.New("connection refused"), want: codes.Unavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, locateStatusCode(tc.err))
		})
	}
}

func TestHeartbeatClassifiesLocatorFailures(t *testing.T) {
	for _, test := range []struct {
		name       string
		err        error
		wantCode   codes.Code
		wantClosed bool
	}{
		{name: "unavailable", err: errors.New("redis unavailable")},
		{name: "deadline", err: context.DeadlineExceeded},
		{name: "canceled", err: context.Canceled, wantCode: codes.Canceled},
		{name: "missing", err: locate.ErrGateNotFound, wantCode: codes.Aborted, wantClosed: true},
		{name: "binding changed", err: locate.ErrGateConflict, wantCode: codes.Aborted, wantClosed: true},
		{name: "invalid lease", err: locate.ErrInvalidGateLease, wantCode: codes.Internal, wantClosed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &renewLeaseLocator{err: test.err}
			gateway := &Server{locator: store, leaseTimeout: time.Second, leaseTTL: time.Minute}
			conn := newTestConnection("conn-a")
			sess := activeSession(conn, testBinding())
			sess.leaseDeadline = time.Now().Add(gateway.leaseTTL / 2)

			err := gateway.heartbeat(context.Background(), sess)

			require.Equal(t, test.wantCode, status.Code(err))
			require.Equal(t, test.wantClosed, isClosed(conn.closed)())
			require.Equal(t, int32(1), store.calls.Load())
		})
	}
}

func TestHeartbeatRenewsAtHalfTTLAndRetriesTransientFailure(t *testing.T) {
	store := &renewLeaseLocator{errors: []error{context.DeadlineExceeded, nil}}
	gateway := &Server{locator: store, leaseTimeout: time.Second, leaseTTL: time.Minute}
	sess := activeSession(newTestConnection("conn-a"), testBinding())
	sess.leaseDeadline = time.Now().Add(gateway.leaseTTL / 2)
	initialDeadline := sess.leaseDeadline

	require.NoError(t, gateway.heartbeat(context.Background(), sess))
	require.Equal(t, initialDeadline, sess.leaseDeadline)
	require.NoError(t, gateway.heartbeat(context.Background(), sess))
	require.Greater(t, sess.leaseDeadline, initialDeadline)
	require.NoError(t, gateway.heartbeat(context.Background(), sess))
	require.Equal(t, int32(2), store.calls.Load())
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

func TestLeaseTimeoutPreservesShorterParent(t *testing.T) {
	for _, test := range []struct {
		name   string
		parent time.Duration
		lease  time.Duration
		limit  time.Duration
	}{
		{name: "parent", parent: time.Second, lease: 2 * time.Second, limit: time.Second},
		{name: "lease", parent: 2 * time.Second, lease: time.Second, limit: time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := testLocator(t)
			store := &contextCheckedRenewLocator{Locator: base}
			gate := newTestServer(t, Locator(store), RPCTimeout(time.Nanosecond), LeaseTimeout(test.lease))
			t.Cleanup(func() { require.NoError(t, gate.Stop(context.Background())) })
			lease, _, err := base.BindGate(context.Background(), testBinding(), time.Minute)
			require.NoError(t, err)
			sess := activeSession(newTestConnection("conn-a"), lease.Binding)
			deadline := time.Now().Add(gate.leaseTTL / 2)
			sess.leaseDeadline = deadline
			ctx, cancel := context.WithTimeout(context.Background(), test.parent)
			defer cancel()
			require.NoError(t, gate.heartbeat(ctx, sess))
			require.Greater(t, store.remaining, test.limit/2)
			require.LessOrEqual(t, store.remaining, test.limit)
			require.True(t, sess.leaseDeadline.After(deadline), "request timeout must not prevent independent renewal")
		})
	}
}

func benchmarkRenewalWave(b *testing.B, connectionCount int, delay, leaseTimeout time.Duration) {
	b.Helper()
	store := &renewalWaveLocator{delay: delay}
	server := &Server{locator: store, leaseTimeout: leaseTimeout, leaseTTL: time.Minute}
	sessions := renewalWaveSessions(connectionCount, server.leaseTTL)

	b.ReportAllocs()
	b.ResetTimer()
	started := time.Now()
	for range b.N {
		runRenewalWave(server, sessions)
	}
	elapsed := time.Since(started)
	b.StopTimer()

	calls := store.calls.Load()
	b.ReportMetric(float64(calls)/float64(b.N), "renewals/wave")
	b.ReportMetric(float64(calls)/elapsed.Seconds(), "renewals/s")
	b.ReportMetric(float64(store.maxActive.Load()), "max-active")
}

func renewalWaveSessions(count int, leaseTTL time.Duration) []*session {
	sessions := make([]*session, count)
	deadline := time.Now().Add(leaseTTL / 2)
	for index := range sessions {
		binding := testBinding()
		binding.UID = fmt.Sprintf("player-%d", index)
		binding.ConnID = fmt.Sprintf("conn-%d", index)
		binding.BindingToken = fmt.Sprintf("binding-%d", index)
		sessions[index] = activeSession(newTestConnection(binding.ConnID), binding)
		sessions[index].leaseDeadline = deadline
	}
	return sessions
}

func runRenewalWave(server *Server, sessions []*session) {
	var wait sync.WaitGroup
	wait.Add(len(sessions))
	start := make(chan struct{})
	for _, sess := range sessions {
		go func() {
			defer wait.Done()
			<-start
			_ = server.heartbeat(context.Background(), sess)
		}()
	}
	close(start)
	wait.Wait()
}

type renewalWaveLocator struct {
	locate.Locator
	delay     time.Duration
	calls     atomic.Int64
	active    atomic.Int64
	maxActive atomic.Int64
}

func (s *renewalWaveLocator) RenewGateLease(ctx context.Context, _ locate.GateBinding, _ time.Duration) (locate.GateLease, error) {
	s.calls.Add(1)
	active := s.active.Add(1)
	defer s.active.Add(-1)
	for maximum := s.maxActive.Load(); active > maximum; maximum = s.maxActive.Load() {
		if s.maxActive.CompareAndSwap(maximum, active) {
			break
		}
	}
	if s.delay > 0 {
		timer := time.NewTimer(s.delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return locate.GateLease{}, ctx.Err()
		}
	}
	return locate.GateLease{}, errRenewalUnavailable
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

type renewLeaseLocator struct {
	locate.Locator
	err    error
	errors []error
	calls  atomic.Int32
}

func (s *renewLeaseLocator) RenewGateLease(_ context.Context, binding locate.GateBinding, ttl time.Duration) (locate.GateLease, error) {
	call := int(s.calls.Add(1)) - 1
	err := s.err
	if call < len(s.errors) {
		err = s.errors[call]
	}
	if err != nil {
		return locate.GateLease{}, err
	}
	return locate.GateLease{Binding: binding, TTL: ttl}, nil
}

type contextCheckedRenewLocator struct {
	locate.Locator
	remaining time.Duration
}

func (l *contextCheckedRenewLocator) RenewGateLease(ctx context.Context, binding locate.GateBinding, ttl time.Duration) (locate.GateLease, error) {
	if deadline, ok := ctx.Deadline(); ok {
		l.remaining = time.Until(deadline)
	}
	if err := ctx.Err(); err != nil {
		return locate.GateLease{}, err
	}
	return l.Locator.RenewGateLease(ctx, binding, ttl)
}
