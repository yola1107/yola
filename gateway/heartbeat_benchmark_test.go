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

func benchmarkRenewalWave(b *testing.B, connectionCount int, delay, rpcTimeout time.Duration) {
	b.Helper()
	store := &renewalWaveLocator{delay: delay}
	store.failing.Store(true)
	server := &Server{locator: store, rpcTimeout: rpcTimeout, leaseTTL: time.Minute}
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
	failing   atomic.Bool
	calls     atomic.Int64
	active    atomic.Int64
	maxActive atomic.Int64
}

func (s *renewalWaveLocator) RenewGateLease(ctx context.Context, binding locate.GateBinding, ttl time.Duration) (locate.GateLease, error) {
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
	if s.failing.Load() {
		return locate.GateLease{}, errRenewalUnavailable
	}
	return locate.GateLease{Binding: binding, TTL: ttl}, nil
}
