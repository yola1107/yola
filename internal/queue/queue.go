// Package queue 提供有序、有界的 callback 执行队列。
package queue

import (
	"errors"
	"runtime/debug"
	"sync"
)

var (
	ErrClosed      = errors.New("callback queue is closed") // 队列已停止接纳 callback
	ErrFull        = errors.New("callback queue is full")   // 等待队列已满
	ErrNilCallback = errors.New("callback is nil")          // 提交的 callback 为 nil
)

// PanicHandler 接收 callback 执行时捕获的 panic。
type PanicHandler func(value any, stack []byte)

// Queue 保存 callback，由单个 worker 按提交顺序执行。
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

// Submit 非阻塞提交一个 callback。
func (q *Queue) Submit(fn func()) error {
	return q.SubmitBatch(fn)
}

// SubmitBatch 非阻塞提交整批 callback；任一项为 nil 或容量不足时整批拒绝。
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
		select {
		case q.wake <- struct{}{}:
		default:
		}
	}
	return nil
}

// Stop 拒绝新 callback，并丢弃尚未开始的 callback。
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

// BeginTermination 立即拒绝等待中的工作，返回的函数须在调用方关闭资源后放行终止 callback。
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

// Run 执行 callback 直到 Stop 或 BeginTermination；只允许调用一次。
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
