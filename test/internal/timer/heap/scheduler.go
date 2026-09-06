// Package timerheap provides a high-precision timer backed by a minimum heap.
package timerheap

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"yola/test/internal/timer"
)

const executorRetryDelay = 10 * time.Millisecond

var _ timer.Scheduler = (*Scheduler)(nil)

type state uint8

const (
	stateNew state = iota
	stateRunning
	stateStopping
	stateStopped
)

// Option configures a Scheduler.
type Option func(*options)

type options struct {
	executor timer.Executor
	logger   *slog.Logger
}

// WithExecutor sets how due callbacks are executed.
func WithExecutor(executor timer.Executor) Option {
	return func(opts *options) { opts.executor = executor }
}

// WithLogger sets callback panic and executor rejection logging.
func WithLogger(logger *slog.Logger) Option {
	return func(opts *options) { opts.logger = logger }
}

// Scheduler orders callbacks by absolute execution time.
type Scheduler struct {
	executor timer.Executor
	logger   *slog.Logger

	mu      sync.Mutex
	state   state
	queue   taskHeap
	tasks   map[timer.TaskID]*taskEntry
	wakeup  chan struct{}
	stop    chan struct{}
	done    chan struct{}
	stopOne sync.Once
	doneOne sync.Once

	nextID    atomic.Int64
	running   atomic.Int32
	callbacks sync.WaitGroup
}

// New creates a scheduler. Tasks may be registered before Start.
func New(opts ...Option) (*Scheduler, error) {
	o := options{
		executor: timer.ExecutorFunc(func(callback func()) error {
			go callback()
			return nil
		}),
	}
	for _, opt := range opts {
		opt(&o)
	}
	if o.logger == nil {
		o.logger = slog.Default()
	}
	if o.executor == nil {
		return nil, errors.New("heap timer executor is nil")
	}
	return &Scheduler{
		executor: o.executor,
		logger:   o.logger,
		queue:    make(taskHeap, 0),
		tasks:    make(map[timer.TaskID]*taskEntry),
		wakeup:   make(chan struct{}, 1),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}, nil
}

// Start starts the scheduler loop and returns after it is ready.
func (s *Scheduler) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("start heap timer: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("start heap timer: %w", err)
	}

	s.mu.Lock()
	switch s.state {
	case stateNew:
		s.state = stateRunning
	case stateRunning:
		s.mu.Unlock()
		return timer.ErrStarted
	default:
		s.mu.Unlock()
		return timer.ErrStopped
	}
	s.mu.Unlock()

	go s.run(ctx)
	return nil
}

// Stop stops accepting tasks and waits for submitted callbacks or ctx cancellation.
func (s *Scheduler) Stop(ctx context.Context) error {
	if ctx == nil {
		return errors.New("stop heap timer: context is nil")
	}
	s.requestStop()
	select {
	case <-s.done:
		return nil
	default:
	}
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		select {
		case <-s.done:
			return nil
		default:
			return fmt.Errorf("stop heap timer: %w", ctx.Err())
		}
	}
}

// Once registers a one-shot callback.
func (s *Scheduler) Once(delay time.Duration, callback func()) (timer.TaskID, error) {
	if delay < 0 {
		return 0, errors.New("heap timer delay is negative")
	}
	return s.schedule(delay, 0, false, callback)
}

// Forever registers a fixed-rate callback whose first run is after interval.
func (s *Scheduler) Forever(interval time.Duration, callback func()) (timer.TaskID, error) {
	return s.schedule(interval, interval, true, callback)
}

// ForeverNow registers a fixed-rate callback whose first run is immediate.
func (s *Scheduler) ForeverNow(interval time.Duration, callback func()) (timer.TaskID, error) {
	return s.schedule(0, interval, true, callback)
}

// Cancel cancels a pending or periodic task.
func (s *Scheduler) Cancel(id timer.TaskID) bool {
	s.mu.Lock()
	entry, ok := s.tasks[id]
	if ok {
		entry.canceled.Store(true)
		delete(s.tasks, id)
		s.queue.remove(entry)
	}
	s.mu.Unlock()
	if ok {
		s.notify()
	}
	return ok
}

// CancelAll cancels all pending and periodic tasks.
func (s *Scheduler) CancelAll() {
	s.mu.Lock()
	for _, entry := range s.tasks {
		entry.canceled.Store(true)
	}
	clear(s.tasks)
	clear(s.queue)
	s.queue = s.queue[:0]
	s.mu.Unlock()
	s.notify()
}

// Monitor returns scheduler utilization.
func (s *Scheduler) Monitor() timer.Monitor {
	s.mu.Lock()
	total := len(s.tasks)
	s.mu.Unlock()
	return timer.Monitor{Total: total, Running: int(s.running.Load())}
}

func (s *Scheduler) schedule(delay, interval time.Duration, repeated bool, callback func()) (timer.TaskID, error) {
	if callback == nil {
		return 0, errors.New("heap timer callback is nil")
	}
	if repeated && interval <= 0 {
		return 0, errors.New("heap timer interval must be positive")
	}
	entry := &taskEntry{
		id:       timer.TaskID(s.nextID.Add(1)),
		execAt:   time.Now().Add(delay),
		interval: interval,
		repeated: repeated,
		callback: callback,
		index:    -1,
	}

	s.mu.Lock()
	if s.state == stateStopping || s.state == stateStopped {
		s.mu.Unlock()
		return 0, timer.ErrStopped
	}
	s.tasks[entry.id] = entry
	heap.Push(&s.queue, entry)
	s.mu.Unlock()
	s.notify()
	return entry.id, nil
}

func (s *Scheduler) run(ctx context.Context) {
	wakeTimer := time.NewTimer(time.Hour)
	if !wakeTimer.Stop() {
		<-wakeTimer.C
	}
	defer wakeTimer.Stop()

	for {
		for _, entry := range s.popExpired(time.Now()) {
			s.dispatch(entry)
		}

		delay, scheduled := s.nextDelay(time.Now())
		var timerC <-chan time.Time
		if scheduled {
			wakeTimer.Reset(delay)
			timerC = wakeTimer.C
		}

		select {
		case <-timerC:
		case <-s.wakeup:
			if scheduled && !wakeTimer.Stop() {
				select {
				case <-wakeTimer.C:
				default:
				}
			}
		case <-ctx.Done():
			s.requestStop()
			s.completeStop()
			return
		case <-s.stop:
			s.completeStop()
			return
		}
	}
}

func (s *Scheduler) popExpired(now time.Time) []*taskEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != stateRunning {
		return nil
	}
	var expired []*taskEntry
	for len(s.queue) > 0 && !s.queue[0].execAt.After(now) {
		expired = append(expired, heap.Pop(&s.queue).(*taskEntry))
	}
	return expired
}

func (s *Scheduler) nextDelay(now time.Time) (time.Duration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != stateRunning || len(s.queue) == 0 {
		return 0, false
	}
	delay := s.queue[0].execAt.Sub(now)
	if delay < 0 {
		delay = 0
	}
	return delay, true
}

func (s *Scheduler) dispatch(entry *taskEntry) {
	s.mu.Lock()
	current, ok := s.tasks[entry.id]
	if s.state != stateRunning || !ok || current != entry || entry.canceled.Load() {
		s.mu.Unlock()
		return
	}
	s.callbacks.Add(1)
	s.mu.Unlock()

	job := func() {
		defer s.callbacks.Done()
		s.running.Add(1)
		defer s.running.Add(-1)
		defer s.finish(entry)
		defer func() {
			if recovered := recover(); recovered != nil {
				s.logger.Error("timer callback panic",
					"panic", recovered,
					"stack", string(debug.Stack()),
				)
			}
		}()
		if !entry.canceled.Load() {
			entry.callback()
		}
	}
	if err := s.executor.Submit(job); err != nil {
		s.callbacks.Done()
		s.handleRejected(entry, err)
	}
}

func (s *Scheduler) handleRejected(entry *taskEntry, submitErr error) {
	s.logger.Warn("timer executor rejected callback", "task_id", entry.id, "error", submitErr)

	s.mu.Lock()
	current, ok := s.tasks[entry.id]
	if !ok || current != entry || entry.canceled.Load() || s.state != stateRunning {
		s.mu.Unlock()
		return
	}
	if entry.repeated {
		entry.execAt = nextExecution(entry.execAt, entry.interval, time.Now())
	} else {
		entry.execAt = time.Now().Add(executorRetryDelay)
	}
	heap.Push(&s.queue, entry)
	s.mu.Unlock()
	s.notify()
}

func (s *Scheduler) finish(entry *taskEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.tasks[entry.id]
	if !ok || current != entry || entry.canceled.Load() || s.state != stateRunning {
		return
	}
	if !entry.repeated {
		delete(s.tasks, entry.id)
		return
	}
	entry.execAt = nextExecution(entry.execAt, entry.interval, time.Now())
	heap.Push(&s.queue, entry)
	s.notify()
}

func (s *Scheduler) requestStop() {
	s.mu.Lock()
	switch s.state {
	case stateNew:
		s.state = stateStopped
		s.cancelAllLocked()
		s.stopOne.Do(func() { close(s.stop) })
		s.doneOne.Do(func() { close(s.done) })
	case stateRunning:
		s.state = stateStopping
		s.cancelAllLocked()
		s.stopOne.Do(func() { close(s.stop) })
	}
	s.mu.Unlock()
}

func (s *Scheduler) completeStop() {
	s.callbacks.Wait()
	s.mu.Lock()
	s.state = stateStopped
	s.mu.Unlock()
	s.doneOne.Do(func() { close(s.done) })
}

func (s *Scheduler) cancelAllLocked() {
	for _, entry := range s.tasks {
		entry.canceled.Store(true)
	}
	clear(s.tasks)
	clear(s.queue)
	s.queue = s.queue[:0]
}

func (s *Scheduler) notify() {
	select {
	case s.wakeup <- struct{}{}:
	default:
	}
}

func nextExecution(previous time.Time, interval time.Duration, now time.Time) time.Time {
	next := previous.Add(interval)
	if next.After(now) {
		return next
	}
	missed := int64(now.Sub(next)/interval) + 1
	return next.Add(time.Duration(missed) * interval)
}
