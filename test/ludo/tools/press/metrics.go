package press

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

const (
	latencySampleSize          = 1024
	broadcastLatencySampleRate = 128
)

type startupStage int

const (
	stageConnect startupStage = iota
	stageLogin
	stageScene
	stageReady
	startupStageCount
)

var startupStageNames = [startupStageCount]string{"connect", "login", "scene", "ready"}

type stageMetric struct {
	attempts  atomic.Int64
	succeeded atomic.Int64
	latencies latencySamples
}

type broadcastMetric struct {
	received  atomic.Int64
	invalid   atomic.Int64
	latencies latencySamples
}

type broadcastReport struct {
	Received int64
	Invalid  int64
	P50      time.Duration
	P95      time.Duration
	P99      time.Duration
}

type stageReport struct {
	Attempts    int64
	Succeeded   int64
	Failed      int64
	SuccessRate float64
	P50         time.Duration
	P95         time.Duration
	P99         time.Duration
}

type latencySamples struct {
	mu     sync.Mutex
	values [latencySampleSize]time.Duration
	next   int
	count  int
}

func (m *stageMetric) record(elapsed time.Duration, succeeded bool) {
	m.attempts.Add(1)
	if succeeded {
		m.succeeded.Add(1)
	}
	m.latencies.record(elapsed)
}

func (m *stageMetric) report() stageReport {
	attempts := m.attempts.Load()
	succeeded := m.succeeded.Load()
	p50, p95, p99 := m.latencies.percentiles()
	report := stageReport{
		Attempts: attempts, Succeeded: succeeded, Failed: attempts - succeeded,
		P50: p50, P95: p95, P99: p99,
	}
	if attempts > 0 {
		report.SuccessRate = float64(succeeded) / float64(attempts)
	}
	return report
}

func (m *broadcastMetric) record(sentAt time.Time, valid bool) {
	received := m.received.Add(1)
	if !valid {
		m.invalid.Add(1)
		return
	}
	// Sampling avoids serializing every client callback on the latency ring.
	if received%broadcastLatencySampleRate != 0 {
		return
	}
	elapsed := time.Since(sentAt)
	if elapsed < 0 {
		m.invalid.Add(1)
		return
	}
	m.latencies.record(elapsed)
}

func (m *broadcastMetric) report() broadcastReport {
	p50, p95, p99 := m.latencies.percentiles()
	return broadcastReport{
		Received: m.received.Load(), Invalid: m.invalid.Load(),
		P50: p50, P95: p95, P99: p99,
	}
}

func (s *latencySamples) record(value time.Duration) {
	s.mu.Lock()
	s.values[s.next] = value
	s.next = (s.next + 1) % len(s.values)
	if s.count < len(s.values) {
		s.count++
	}
	s.mu.Unlock()
}

func (s *latencySamples) percentiles() (time.Duration, time.Duration, time.Duration) {
	s.mu.Lock()
	values := append([]time.Duration(nil), s.values[:s.count]...)
	s.mu.Unlock()
	if len(values) == 0 {
		return 0, 0, 0
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	return percentile(values, 50), percentile(values, 95), percentile(values, 99)
}

func percentile(values []time.Duration, percent int) time.Duration {
	index := (len(values)*percent + 99) / 100
	return values[max(index-1, 0)]
}
