// Package queue provides ordered, bounded callback execution.
package queue

import (
	"errors"
	"runtime/debug"
	"sync"
)

var (
	// ErrClosed reports that the queue no longer accepts callbacks.
	ErrClosed = errors.New("callback queue is closed")
	// ErrFull reports that all pending callback slots are occupied.
	ErrFull = errors.New("callback queue is full")
	// ErrNilCallback reports that Submit received a nil callback.
	ErrNilCallback = errors.New("callback is nil")
)

// PanicHandler reports a panic recovered while invoking a callback.
type PanicHandler func(value any, stack []byte)

// Queue stores callbacks until one worker executes them in submission order.
type Queue struct {
	panicHandler PanicHandler
	capacity     int
	wake         chan struct{}
	mu           sync.Mutex
	tasks        []func()
	terminals    []func()
	closed       bool
}

// New 创建队列；capacity 必须为正，panicHandler 可为 nil。
func New(capacity int, panicHandler PanicHandler) *Queue {
	if capacity <= 0 {
		panic("queue: capacity must be positive")
	}
	return &Queue{
		panicHandler: panicHandler,
		capacity:     capacity,
		wake:         make(chan struct{}, 1),
		tasks:        make([]func(), 0, capacity),
	}
}

// Submit adds a callback without blocking.
func (q *Queue) Submit(fn func()) error {
	return q.SubmitBatch(fn)
}

// SubmitBatch adds all callbacks without blocking. It rejects the whole batch
// when any callback is nil or there are not enough pending slots.
func (q *Queue) SubmitBatch(callbacks ...func()) error {
	if hasNilCallback(callbacks) {
		return ErrNilCallback
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return ErrClosed
	}
	if len(callbacks) > q.capacity-len(q.tasks) {
		return ErrFull
	}
	q.tasks = append(q.tasks, callbacks...)
	if len(callbacks) > 0 {
		q.signal()
	}
	return nil
}

// Stop rejects new callbacks and discards callbacks that have not started.
func (q *Queue) Stop() {
	q.closeWithTerminals(nil)
}

func (q *Queue) closeWithTerminals(terminals []func()) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return false
	}
	q.closed = true
	q.tasks = nil
	q.terminals = terminals
	close(q.wake)
	return true
}

// BeginTermination rejects pending work immediately and returns a function that
// releases terminal after the caller has finished closing its owned resources.
func (q *Queue) BeginTermination(terminals ...func()) func() {
	if len(terminals) == 0 || hasNilCallback(terminals) {
		q.Stop()
		return nil
	}
	ready := make(chan struct{})
	gated := make([]func(), 0, len(terminals)+1)
	gated = append(gated, func() { <-ready })
	gated = append(gated, terminals...)
	if !q.closeWithTerminals(gated) {
		return func() { q.invokeAll(terminals) }
	}
	return func() { close(ready) }
}

// Run executes callbacks until Stop or BeginTermination. It must be called once.
func (q *Queue) Run() {
	for {
		callback, terminals, closed := q.next()
		switch {
		case callback != nil:
			q.invokeSafely(callback)
		case closed:
			q.invokeAll(terminals)
			return
		default:
			<-q.wake
		}
	}
}

func (q *Queue) next() (callback func(), terminals []func(), closed bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.closed && len(q.tasks) > 0 {
		callback = q.tasks[0]
		q.tasks[0] = nil
		q.tasks = q.tasks[1:]
		return callback, nil, false
	}
	if q.closed {
		terminals = q.terminals
		q.terminals = nil
		return nil, terminals, true
	}
	return nil, nil, false
}

func (q *Queue) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func hasNilCallback(callbacks []func()) bool {
	for _, callback := range callbacks {
		if callback == nil {
			return true
		}
	}
	return false
}

func (q *Queue) invokeSafely(fn func()) {
	defer func() {
		if value := recover(); value != nil && q.panicHandler != nil {
			q.panicHandler(value, debug.Stack())
		}
	}()
	fn()
}

func (q *Queue) invokeAll(callbacks []func()) {
	for _, callback := range callbacks {
		q.invokeSafely(callback)
	}
}
