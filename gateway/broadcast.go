package gateway

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	protocolv1 "yola/api/protocol/v1"
	"yola/internal/contextwait"
	"yola/network"
)

var (
	ErrBroadcastUnavailable = errors.New("gateway: broadcast is unavailable")
	ErrBroadcastQueueFull   = errors.New("gateway: broadcast queue is full")
)

// BroadcastStats is a point-in-time snapshot of local broadcast admission and fanout.
type BroadcastStats struct {
	QueueDepth         int
	QueueCapacity      int
	Accepted           uint64
	Completed          uint64
	QueueDropped       uint64
	SendDropped        uint64
	LastFanoutDuration time.Duration
	MaxFanoutDuration  time.Duration
}

// Broadcast queues one push for every currently authenticated local session.
func (s *Server) Broadcast(command int32, payload []byte) error {
	message := &protocolv1.Proto{Op: protocolv1.OpPush, Cmd: command, Body: payload}
	if !validExternalFrame(message) {
		return network.ErrFrameTooLarge
	}
	message.Body = append([]byte(nil), payload...)
	return s.broadcaster.enqueue(message)
}

// BroadcastStats returns local broadcaster counters without resetting them.
func (s *Server) BroadcastStats() BroadcastStats {
	return s.broadcaster.stats()
}

type broadcaster struct {
	mu            sync.RWMutex
	sessions      *sessionRegistry
	workers       int
	queueCapacity int
	active        *broadcastRun
	accepted      atomic.Uint64
	completed     atomic.Uint64
	queueDropped  atomic.Uint64
	sendDropped   atomic.Uint64
	lastFanout    atomic.Uint64
	maxFanout     atomic.Uint64
}

type broadcastRun struct {
	ctx       context.Context
	cancel    context.CancelFunc
	pushes    chan *protocolv1.Proto
	batches   chan fanoutBatch
	batchDone chan struct{}
	sessions  []*session
	prepared  network.PreparedProto
	done      chan struct{}
	wg        sync.WaitGroup
}

type fanoutBatch struct {
	message  *network.PreparedProto
	sessions []*session
}

func newBroadcaster(sessions *sessionRegistry, workers, queueCapacity int) *broadcaster {
	return &broadcaster{
		sessions:      sessions,
		workers:       workers,
		queueCapacity: queueCapacity,
	}
}

func (b *broadcaster) start() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.active != nil {
		return errors.New("gateway: broadcaster is already started")
	}
	ctx, cancel := context.WithCancel(context.Background())
	running := &broadcastRun{
		ctx:       ctx,
		cancel:    cancel,
		pushes:    make(chan *protocolv1.Proto, b.queueCapacity),
		batches:   make(chan fanoutBatch, b.workers),
		batchDone: make(chan struct{}, b.workers),
		done:      make(chan struct{}),
	}
	b.active = running
	running.wg.Add(b.workers)
	for range b.workers {
		go b.fanoutWorker(running)
	}
	go b.coordinate(running)
	return nil
}

func (b *broadcaster) finishRun(running *broadcastRun) {
	running.cancel()
	running.wg.Wait()
	b.mu.Lock()
	if b.active == running {
		b.active = nil
	}
	close(running.done)
	b.mu.Unlock()
}

func (b *broadcaster) stop(ctx context.Context) error {
	b.mu.Lock()
	running := b.active
	if running == nil {
		b.mu.Unlock()
		return nil
	}
	running.cancel()
	b.mu.Unlock()

	return contextwait.Done(ctx, running.done)
}

func (b *broadcaster) enqueue(message *protocolv1.Proto) error {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.active == nil || b.active.ctx.Err() != nil {
		return ErrBroadcastUnavailable
	}
	select {
	case b.active.pushes <- message:
		b.accepted.Add(1)
		return nil
	default:
		b.recordDrop(&b.queueDropped, "queue_full")
		return ErrBroadcastQueueFull
	}
}

func (b *broadcaster) coordinate(running *broadcastRun) {
	defer b.finishRun(running)
	// Complete one push before taking the next so every connection observes the same order.
	for {
		select {
		case <-running.ctx.Done():
			return
		case message := <-running.pushes:
			started := time.Now()
			if !b.fanout(running, message) {
				return
			}
			b.recordCompleted(time.Since(started))
		}
	}
}

func (b *broadcaster) fanout(running *broadcastRun, message *protocolv1.Proto) bool {
	running.prepared.Reset(message)
	sessions := b.sessions.snapshot(running.sessions)
	running.sessions = sessions
	batchCount := min(b.workers, len(sessions))
	if batchCount == 0 {
		return true
	}
	batchSize := (len(sessions) + batchCount - 1) / batchCount
	started := 0
	for offset := 0; offset < len(sessions); offset += batchSize {
		end := min(offset+batchSize, len(sessions))
		select {
		case running.batches <- fanoutBatch{
			message: &running.prepared, sessions: sessions[offset:end],
		}:
			started++
		case <-running.ctx.Done():
			return false
		}
	}
	for range started {
		select {
		case <-running.batchDone:
		case <-running.ctx.Done():
			return false
		}
	}
	clear(sessions)
	running.sessions = sessions[:0]
	return true
}

func (b *broadcaster) fanoutWorker(running *broadcastRun) {
	defer running.wg.Done()
	for {
		select {
		case <-running.ctx.Done():
			return
		case batch := <-running.batches:
			b.sendBatch(running.ctx, batch)
			running.batchDone <- struct{}{}
		}
	}
}

func (b *broadcaster) sendBatch(ctx context.Context, batch fanoutBatch) {
	now := time.Now()
	for _, sess := range batch.sessions {
		if ctx.Err() != nil {
			return
		}
		matched, err := sess.sendIfAuthenticated(now, batch.message)
		if !matched || err == nil {
			continue
		}
		reason := "connection_error"
		if errors.Is(err, network.ErrSendQueueFull) {
			reason = "connection_queue_full"
		} else if errors.Is(err, network.ErrConnectionClosed) {
			reason = "connection_closed"
		}
		b.recordDrop(&b.sendDropped, reason)
	}
}

func (b *broadcaster) recordDrop(counter *atomic.Uint64, reason string) {
	dropped := counter.Add(1)
	if dropped&(dropped-1) == 0 {
		slog.Warn("gateway broadcast dropped", "reason", reason, "dropped", dropped)
	}
}

func (b *broadcaster) stats() BroadcastStats {
	b.mu.RLock()
	queueDepth := 0
	if b.active != nil {
		queueDepth = len(b.active.pushes)
	}
	b.mu.RUnlock()
	return BroadcastStats{
		QueueDepth:         queueDepth,
		QueueCapacity:      b.queueCapacity,
		Accepted:           b.accepted.Load(),
		Completed:          b.completed.Load(),
		QueueDropped:       b.queueDropped.Load(),
		SendDropped:        b.sendDropped.Load(),
		LastFanoutDuration: time.Duration(b.lastFanout.Load()),
		MaxFanoutDuration:  time.Duration(b.maxFanout.Load()),
	}
}

func (b *broadcaster) recordCompleted(duration time.Duration) {
	nanoseconds := uint64(duration)
	b.lastFanout.Store(nanoseconds)
	if nanoseconds > b.maxFanout.Load() {
		b.maxFanout.Store(nanoseconds)
	}
	b.completed.Add(1)
}
