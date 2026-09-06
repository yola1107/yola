// Package stdlib provides timers backed by time.AfterFunc.
package stdlib

import (
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
	logger *slog.Logger
}

// WithLogger sets callback panic logging.
func WithLogger(logger *slog.Logger) Option {
	return func(opts *options) { opts.logger = logger }
}

// Scheduler manages standard-library one-shot and fixed-rate timers.
type Scheduler struct {
	logger *slog.Logger

	mu     sync.Mutex
	state  state
	nextID timer.TaskID
	tasks  map[timer.TaskID]*task
	done   chan struct{}

	running   atomic.Int32
	callbacks sync.WaitGroup
	doneOnce  sync.Once
}

type task struct {
	id        timer.TaskID
	execAt    time.Time
	interval  time.Duration
	repeated  bool
	callback  func()
	scheduled *time.Timer
}

// New creates a scheduler. Tasks may be registered before Start.
func New(opts ...Option) *Scheduler {
	o := new(options)
	for _, opt := range opts {
		opt(o)
	}
	if o.logger == nil {
		o.logger = slog.Default()
	}
	return &Scheduler{
		logger: o.logger,
		tasks:  make(map[timer.TaskID]*task),
		done:   make(chan struct{}),
	}
}

// Start arms registered tasks and stops the scheduler when ctx is canceled.
func (s *Scheduler) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("start stdlib timer: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("start stdlib timer: %w", err)
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
	tasks := make([]*task, 0, len(s.tasks))
	for _, scheduled := range s.tasks {
		tasks = append(tasks, scheduled)
	}
	s.mu.Unlock()

	go func() {
		select {
		case <-ctx.Done():
			s.requestStop()
		case <-s.done:
		}
	}()
	for _, scheduled := range tasks {
		s.arm(scheduled)
	}
	return nil
}

// Stop cancels pending tasks and waits for running callbacks or ctx cancellation.
func (s *Scheduler) Stop(ctx context.Context) error {
	if ctx == nil {
		return errors.New("stop stdlib timer: context is nil")
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
			return fmt.Errorf("stop stdlib timer: %w", ctx.Err())
		}
	}
}

// Once registers a one-shot callback.
func (s *Scheduler) Once(delay time.Duration, callback func()) (timer.TaskID, error) {
	if delay < 0 {
		return 0, errors.New("stdlib timer delay is negative")
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

// Cancel prevents a pending callback or future periodic runs; a running callback completes.
func (s *Scheduler) Cancel(id timer.TaskID) bool {
	s.mu.Lock()
	scheduled, ok := s.tasks[id]
	var taskTimer *time.Timer
	if ok {
		delete(s.tasks, id)
		taskTimer = scheduled.scheduled
		scheduled.scheduled = nil
	}
	s.mu.Unlock()
	if taskTimer != nil {
		taskTimer.Stop()
	}
	return ok
}

// CancelAll prevents all pending callbacks and future periodic runs.
func (s *Scheduler) CancelAll() {
	for _, scheduled := range s.cancelAll() {
		scheduled.Stop()
	}
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
		return 0, errors.New("stdlib timer callback is nil")
	}
	if repeated && interval <= 0 {
		return 0, errors.New("stdlib timer interval must be positive")
	}

	s.mu.Lock()
	if s.state == stateStopping || s.state == stateStopped {
		s.mu.Unlock()
		return 0, timer.ErrStopped
	}
	s.nextID++
	scheduled := &task{
		id:       s.nextID,
		execAt:   time.Now().Add(delay),
		interval: interval,
		repeated: repeated,
		callback: callback,
	}
	s.tasks[scheduled.id] = scheduled
	running := s.state == stateRunning
	s.mu.Unlock()
	if running {
		s.arm(scheduled)
	}
	return scheduled.id, nil
}

func (s *Scheduler) arm(scheduled *task) {
	s.mu.Lock()
	current, ok := s.tasks[scheduled.id]
	if s.state != stateRunning || !ok || current != scheduled || scheduled.scheduled != nil {
		s.mu.Unlock()
		return
	}
	delay := time.Until(scheduled.execAt)
	if delay < 0 {
		delay = 0
	}
	scheduled.scheduled = time.AfterFunc(delay, func() { s.execute(scheduled) })
	s.mu.Unlock()
}

func (s *Scheduler) execute(scheduled *task) {
	s.mu.Lock()
	current, ok := s.tasks[scheduled.id]
	if s.state != stateRunning || !ok || current != scheduled {
		s.mu.Unlock()
		return
	}
	scheduled.scheduled = nil
	s.callbacks.Add(1)
	s.mu.Unlock()

	s.running.Add(1)
	defer s.callbacks.Done()
	defer s.running.Add(-1)
	defer s.finish(scheduled)
	defer func() {
		if recovered := recover(); recovered != nil {
			s.logger.Error("timer callback panic",
				"task_id", scheduled.id,
				"panic", recovered,
				"stack", string(debug.Stack()),
			)
		}
	}()
	scheduled.callback()
}

func (s *Scheduler) finish(scheduled *task) {
	s.mu.Lock()
	current, ok := s.tasks[scheduled.id]
	if s.state != stateRunning || !ok || current != scheduled {
		s.mu.Unlock()
		return
	}
	if !scheduled.repeated {
		delete(s.tasks, scheduled.id)
		s.mu.Unlock()
		return
	}
	scheduled.execAt = nextExecution(scheduled.execAt, scheduled.interval, time.Now())
	s.mu.Unlock()
	s.arm(scheduled)
}

func (s *Scheduler) requestStop() {
	s.mu.Lock()
	stateBeforeStop := s.state
	switch s.state {
	case stateNew:
		s.state = stateStopped
	case stateRunning:
		s.state = stateStopping
	default:
		s.mu.Unlock()
		return
	}
	scheduled := s.cancelAllLocked()
	s.mu.Unlock()

	for _, taskTimer := range scheduled {
		taskTimer.Stop()
	}
	if stateBeforeStop == stateNew {
		s.doneOnce.Do(func() { close(s.done) })
		return
	}
	go s.completeStop()
}

func (s *Scheduler) completeStop() {
	s.callbacks.Wait()
	s.mu.Lock()
	s.state = stateStopped
	s.mu.Unlock()
	s.doneOnce.Do(func() { close(s.done) })
}

func (s *Scheduler) cancelAll() []*time.Timer {
	s.mu.Lock()
	scheduled := s.cancelAllLocked()
	s.mu.Unlock()
	return scheduled
}

func (s *Scheduler) cancelAllLocked() []*time.Timer {
	scheduled := make([]*time.Timer, 0, len(s.tasks))
	for _, taskEntry := range s.tasks {
		if taskEntry.scheduled != nil {
			scheduled = append(scheduled, taskEntry.scheduled)
			taskEntry.scheduled = nil
		}
	}
	clear(s.tasks)
	return scheduled
}

func nextExecution(previous time.Time, interval time.Duration, now time.Time) time.Time {
	next := previous.Add(interval)
	if next.After(now) {
		return next
	}
	missed := int64(now.Sub(next)/interval) + 1
	return next.Add(time.Duration(missed) * interval)
}
