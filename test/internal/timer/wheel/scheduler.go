// Package wheel provides a timer backed by a hierarchical timing wheel.
package wheel

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

	"github.com/RussellLuo/timingwheel"
)

const (
	defaultTick        = 500 * time.Millisecond
	defaultWheelSize   = 128
	minimumWheelSize   = 2
	executorRetryDelay = 10 * time.Millisecond
)

var _ timer.Scheduler = (*Scheduler)(nil)

type state uint8

const (
	stateNew state = iota
	stateRunning
	stateStopping
	stateStopped
)

type taskEntry struct {
	id       timer.TaskID
	execAt   time.Time
	interval time.Duration
	repeated bool
	canceled atomic.Bool
	callback func()
	timer    *timingwheel.Timer
	arming   bool
}

// Option configures a Scheduler.
type Option func(*options)

type options struct {
	tick      time.Duration
	wheelSize int64
	executor  timer.Executor
	logger    *slog.Logger
}

// WithTick sets timing-wheel precision; it must be at least one millisecond.
func WithTick(tick time.Duration) Option {
	return func(opts *options) { opts.tick = tick }
}

// WithWheelSize sets the number of slots per wheel level.
func WithWheelSize(size int64) Option {
	return func(opts *options) { opts.wheelSize = size }
}

// WithExecutor sets how due callbacks are executed.
func WithExecutor(executor timer.Executor) Option {
	return func(opts *options) { opts.executor = executor }
}

// WithLogger sets callback panic and executor rejection logging.
func WithLogger(logger *slog.Logger) Option {
	return func(opts *options) { opts.logger = logger }
}

// Scheduler efficiently schedules large numbers of lower-precision callbacks.
type Scheduler struct {
	wheel    *timingwheel.TimingWheel
	tick     time.Duration
	executor timer.Executor
	logger   *slog.Logger

	engineMu sync.RWMutex
	mu       sync.Mutex
	state    state
	tasks    map[timer.TaskID]*taskEntry
	stop     chan struct{}
	done     chan struct{}
	stopOne  sync.Once
	doneOne  sync.Once

	nextID    atomic.Int64
	running   atomic.Int32
	callbacks sync.WaitGroup
}

// New creates a scheduler. Tasks may be registered before Start.
func New(opts ...Option) (*Scheduler, error) {
	o := options{
		tick:      defaultTick,
		wheelSize: defaultWheelSize,
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
	if o.tick < time.Millisecond {
		return nil, errors.New("time wheel tick must be at least 1ms")
	}
	if o.wheelSize < minimumWheelSize {
		return nil, errors.New("time wheel size must be greater than 1")
	}
	if o.executor == nil {
		return nil, errors.New("time wheel executor is nil")
	}
	return &Scheduler{
		wheel:    timingwheel.NewTimingWheel(o.tick, o.wheelSize),
		tick:     o.tick,
		executor: o.executor,
		logger:   o.logger,
		tasks:    make(map[timer.TaskID]*taskEntry),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}, nil
}

// Start starts the timing wheel and returns after it is ready.
func (s *Scheduler) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("start time wheel: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("start time wheel: %w", err)
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
	entries := make([]*taskEntry, 0, len(s.tasks))
	for _, entry := range s.tasks {
		entries = append(entries, entry)
	}
	s.mu.Unlock()

	s.engineMu.Lock()
	s.wheel.Start()
	s.engineMu.Unlock()
	for _, entry := range entries {
		s.arm(entry)
	}
	go s.run(ctx)
	return nil
}

// Stop stops accepting tasks and waits for submitted callbacks or ctx cancellation.
func (s *Scheduler) Stop(ctx context.Context) error {
	if ctx == nil {
		return errors.New("stop time wheel: context is nil")
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
			return fmt.Errorf("stop time wheel: %w", ctx.Err())
		}
	}
}

// Once registers a one-shot callback.
func (s *Scheduler) Once(delay time.Duration, callback func()) (timer.TaskID, error) {
	if delay < 0 {
		return 0, errors.New("time wheel delay is negative")
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
	}
	var scheduled *timingwheel.Timer
	if ok {
		scheduled = entry.timer
		entry.timer = nil
	}
	s.mu.Unlock()
	if scheduled != nil {
		scheduled.Stop()
	}
	return ok
}

// CancelAll cancels all pending and periodic tasks.
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
		return 0, errors.New("time wheel callback is nil")
	}
	if repeated && interval <= 0 {
		return 0, errors.New("time wheel interval must be positive")
	}
	entry := &taskEntry{
		id:       timer.TaskID(s.nextID.Add(1)),
		execAt:   time.Now().Add(delay),
		interval: interval,
		repeated: repeated,
		callback: callback,
	}

	s.mu.Lock()
	if s.state == stateStopping || s.state == stateStopped {
		s.mu.Unlock()
		return 0, timer.ErrStopped
	}
	s.tasks[entry.id] = entry
	running := s.state == stateRunning
	s.mu.Unlock()
	if running {
		s.arm(entry)
	}
	return entry.id, nil
}

func (s *Scheduler) run(ctx context.Context) {
	select {
	case <-ctx.Done():
		s.requestStop()
	case <-s.stop:
	}

	s.engineMu.Lock()
	s.wheel.Stop()
	s.engineMu.Unlock()
	s.callbacks.Wait()
	s.mu.Lock()
	s.state = stateStopped
	s.mu.Unlock()
	s.doneOne.Do(func() { close(s.done) })
}

func (s *Scheduler) arm(entry *taskEntry) {
	s.mu.Lock()
	current, ok := s.tasks[entry.id]
	if s.state != stateRunning || !ok || current != entry || entry.canceled.Load() || entry.timer != nil || entry.arming {
		s.mu.Unlock()
		return
	}
	entry.arming = true
	delay := time.Until(entry.execAt)
	if delay < 0 {
		delay = 0
	} else if delay > 0 {
		// timingwheel rounds expiration down to a slot boundary. Advancing one
		// tick keeps lower-precision callbacks from running before execAt.
		delay += s.tick
	}
	s.mu.Unlock()

	ready := make(chan struct{})
	s.engineMu.RLock()
	scheduled := s.wheel.AfterFunc(delay, func() {
		<-ready
		s.dispatch(entry)
	})
	s.engineMu.RUnlock()

	s.mu.Lock()
	entry.arming = false
	current, ok = s.tasks[entry.id]
	if s.state == stateRunning && ok && current == entry && !entry.canceled.Load() {
		entry.timer = scheduled
		s.mu.Unlock()
		close(ready)
		return
	}
	s.mu.Unlock()
	scheduled.Stop()
	close(ready)
}

func (s *Scheduler) dispatch(entry *taskEntry) {
	s.mu.Lock()
	current, ok := s.tasks[entry.id]
	if s.state != stateRunning || !ok || current != entry || entry.canceled.Load() {
		s.mu.Unlock()
		return
	}
	entry.timer = nil
	if time.Now().Before(entry.execAt) {
		s.mu.Unlock()
		s.arm(entry)
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
	s.mu.Unlock()
	s.arm(entry)
}

func (s *Scheduler) finish(entry *taskEntry) {
	s.mu.Lock()
	current, ok := s.tasks[entry.id]
	if !ok || current != entry || entry.canceled.Load() || s.state != stateRunning {
		s.mu.Unlock()
		return
	}
	if !entry.repeated {
		delete(s.tasks, entry.id)
		s.mu.Unlock()
		return
	}
	entry.execAt = nextExecution(entry.execAt, entry.interval, time.Now())
	s.mu.Unlock()
	s.arm(entry)
}

func (s *Scheduler) requestStop() {
	s.mu.Lock()
	first := false
	switch s.state {
	case stateNew:
		s.state = stateStopped
		first = true
	case stateRunning:
		s.state = stateStopping
		first = true
	}
	var scheduled []*timingwheel.Timer
	if first {
		for _, entry := range s.tasks {
			entry.canceled.Store(true)
			if entry.timer != nil {
				scheduled = append(scheduled, entry.timer)
			}
		}
		clear(s.tasks)
	}
	s.mu.Unlock()

	for _, task := range scheduled {
		task.Stop()
	}
	if !first {
		return
	}
	s.stopOne.Do(func() { close(s.stop) })
	s.mu.Lock()
	stoppedBeforeStart := s.state == stateStopped
	s.mu.Unlock()
	if stoppedBeforeStart {
		s.doneOne.Do(func() { close(s.done) })
	}
}

func (s *Scheduler) cancelAll() []*timingwheel.Timer {
	s.mu.Lock()
	defer s.mu.Unlock()
	scheduled := make([]*timingwheel.Timer, 0, len(s.tasks))
	for _, entry := range s.tasks {
		entry.canceled.Store(true)
		if entry.timer != nil {
			scheduled = append(scheduled, entry.timer)
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
