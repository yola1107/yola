// Package ants provides a bounded worker pool backed by panjf2000/ants.
package ants

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	antslib "github.com/panjf2000/ants/v2"
)

const (
	defaultSize       = 100
	defaultExpiryTime = 30 * time.Second
)

var (
	ErrStarted    = errors.New("ants worker pool already started")
	ErrNotStarted = errors.New("ants worker pool not started")
	ErrClosed     = errors.New("ants worker pool closed")
	ErrFull       = errors.New("ants worker pool full")
)

// Monitor is a snapshot of worker-pool utilization.
type Monitor struct {
	Capacity int
	Running  int
	Free     int
	Waiting  int
}

// Option configures a Pool.
type Option func(*options)

type options struct {
	size             int
	expiryTime       time.Duration
	nonblocking      bool
	maxBlockingTasks int
	logger           *slog.Logger
}

// WithSize sets the maximum number of worker goroutines.
func WithSize(size int) Option {
	return func(opts *options) { opts.size = size }
}

// WithExpiryTime sets how long an idle worker is retained.
func WithExpiryTime(expiry time.Duration) Option {
	return func(opts *options) { opts.expiryTime = expiry }
}

// WithNonblocking controls whether a full pool immediately returns ErrFull.
func WithNonblocking(enabled bool) Option {
	return func(opts *options) { opts.nonblocking = enabled }
}

// WithMaxBlockingTasks limits submitters waiting for a worker; zero means unlimited.
func WithMaxBlockingTasks(maximum int) Option {
	return func(opts *options) { opts.maxBlockingTasks = maximum }
}

// WithLogger sets the logger used when a task panics.
func WithLogger(logger *slog.Logger) Option {
	return func(opts *options) { opts.logger = logger }
}

type state uint8

const (
	stateNew state = iota
	stateRunning
	stateStopping
	stateStopped
)

// Pool limits and reuses goroutines for asynchronous work.
type Pool struct {
	options options

	mu       sync.Mutex
	pool     *antslib.Pool
	state    state
	submitWG sync.WaitGroup
	done     chan struct{}
	stopOnce sync.Once
	stopErr  error
}

// New creates a pool. Start must be called before Submit.
func New(opts ...Option) (*Pool, error) {
	o := options{
		size:        defaultSize,
		expiryTime:  defaultExpiryTime,
		nonblocking: true,
		logger:      slog.Default(),
	}
	for _, opt := range opts {
		opt(&o)
	}
	if o.logger == nil {
		o.logger = slog.Default()
	}
	if o.size <= 0 {
		return nil, errors.New("ants worker pool size must be positive")
	}
	if o.expiryTime <= 0 {
		return nil, errors.New("ants worker pool expiry time must be positive")
	}
	if o.maxBlockingTasks < 0 {
		return nil, errors.New("ants worker pool max blocking tasks cannot be negative")
	}

	return &Pool{options: o, done: make(chan struct{})}, nil
}

// Start marks the pool ready and binds its lifetime to ctx.
func (p *Pool) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("start ants worker pool: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("start ants worker pool: %w", err)
	}

	p.mu.Lock()
	switch p.state {
	case stateNew:
	case stateRunning:
		p.mu.Unlock()
		return ErrStarted
	default:
		p.mu.Unlock()
		return ErrClosed
	}
	pool, err := antslib.NewPool(p.options.size,
		antslib.WithExpiryDuration(p.options.expiryTime),
		antslib.WithNonblocking(p.options.nonblocking),
		antslib.WithMaxBlockingTasks(p.options.maxBlockingTasks),
	)
	if err != nil {
		p.mu.Unlock()
		return fmt.Errorf("start ants worker pool: %w", err)
	}
	p.pool = pool
	p.state = stateRunning
	p.mu.Unlock()

	go func() {
		select {
		case <-ctx.Done():
			_ = p.Stop(context.Background())
		case <-p.done:
		}
	}()
	return nil
}

// Stop rejects new tasks and waits for running tasks or ctx cancellation.
func (p *Pool) Stop(ctx context.Context) error {
	if ctx == nil {
		return errors.New("stop ants worker pool: context is nil")
	}
	p.shutdown()
	select {
	case <-p.done:
		return p.stopErr
	default:
	}
	select {
	case <-p.done:
		return p.stopErr
	case <-ctx.Done():
		select {
		case <-p.done:
			return p.stopErr
		default:
			return fmt.Errorf("stop ants worker pool: %w", ctx.Err())
		}
	}
}

// Submit queues task for execution.
func (p *Pool) Submit(task func()) error {
	if task == nil {
		return errors.New("ants worker pool task is nil")
	}
	p.mu.Lock()
	current := p.state
	pool := p.pool
	if current == stateRunning {
		p.submitWG.Add(1)
	}
	p.mu.Unlock()
	switch current {
	case stateNew:
		return ErrNotStarted
	case stateStopping, stateStopped:
		return ErrClosed
	}
	defer p.submitWG.Done()

	err := pool.Submit(func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				p.options.logger.Error("worker pool task panic",
					"panic", recovered,
					"stack", string(debug.Stack()),
				)
			}
		}()
		task()
	})
	if errors.Is(err, antslib.ErrPoolOverload) {
		return ErrFull
	}
	if errors.Is(err, antslib.ErrPoolClosed) {
		return ErrClosed
	}
	if err != nil {
		return fmt.Errorf("submit ants worker pool task: %w", err)
	}
	return nil
}

// Monitor returns current pool utilization.
func (p *Pool) Monitor() Monitor {
	p.mu.Lock()
	pool := p.pool
	running := p.state == stateRunning
	p.mu.Unlock()
	if !running || pool == nil {
		return Monitor{}
	}
	return Monitor{
		Capacity: pool.Cap(),
		Running:  pool.Running(),
		Free:     pool.Free(),
		Waiting:  pool.Waiting(),
	}
}

func (p *Pool) shutdown() {
	p.stopOnce.Do(func() {
		p.mu.Lock()
		if p.state == stateNew {
			p.state = stateStopped
			close(p.done)
			p.mu.Unlock()
			return
		}
		p.state = stateStopping
		pool := p.pool
		p.mu.Unlock()

		go func() {
			p.submitWG.Wait()
			err := pool.ReleaseContext(context.Background())
			if err != nil && !errors.Is(err, antslib.ErrPoolClosed) {
				p.stopErr = fmt.Errorf("release ants worker pool: %w", err)
			}
			p.mu.Lock()
			p.state = stateStopped
			p.mu.Unlock()
			close(p.done)
		}()
	})
}
