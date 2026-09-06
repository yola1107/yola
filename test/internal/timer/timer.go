// Package timer defines in-process delayed task scheduling.
package timer

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrStarted indicates that the scheduler has already started.
	ErrStarted = errors.New("timer scheduler already started")
	// ErrStopped indicates that the scheduler no longer accepts tasks.
	ErrStopped = errors.New("timer scheduler stopped")
)

// TaskID identifies a scheduled task.
type TaskID int64

// Monitor is a snapshot of scheduler utilization.
type Monitor struct {
	Total   int
	Running int
}

// Executor decides how due callbacks are executed.
// Submit must return promptly and return an error only when the callback was not accepted.
// The scheduler never executes a rejected callback outside the Executor: a one-shot
// callback is retried after a short delay, while a periodic occurrence is skipped.
type Executor interface {
	Submit(func()) error
}

// ExecutorFunc adapts a function to Executor.
type ExecutorFunc func(func()) error

// Submit submits a callback.
func (f ExecutorFunc) Submit(callback func()) error {
	return f(callback)
}

// Scheduler schedules one-shot and fixed-rate callbacks.
// Periodic callbacks skip missed intervals and never overlap with themselves.
type Scheduler interface {
	Start(context.Context) error
	Stop(context.Context) error
	Monitor() Monitor
	Once(time.Duration, func()) (TaskID, error)
	Forever(time.Duration, func()) (TaskID, error)
	ForeverNow(time.Duration, func()) (TaskID, error)
	Cancel(TaskID) bool
	CancelAll()
}
