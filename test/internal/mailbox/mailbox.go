// Package mailbox provides bounded FIFO execution for independent logical mailboxes.
package mailbox

import (
	"context"
	"errors"
	"log/slog"
	"runtime/debug"

	kerrors "github.com/go-kratos/kratos/v3/errors"
	"github.com/go-kratos/kratos/v3/middleware"
)

var (
	ErrNotStarted = errors.New("mailbox is not started")
	ErrStopped    = errors.New("mailbox is stopped")
	ErrFull       = errors.New("mailbox queue is full")
	ErrNilJob     = errors.New("mailbox job is nil")
)

// Executor submits work to one FIFO execution context.
type Executor interface {
	TryPost(func()) error
	Call(context.Context, func() error) error
	Stats() Stats
}

// Stats is a snapshot of one executor's queue utilization.
type Stats struct {
	Capacity int
	Running  int
	Free     int
}

// Middleware serializes handler execution through one mailbox.
func Middleware(executor Executor) middleware.Middleware {
	if executor == nil {
		panic("mailbox: nil executor")
	}
	return func(next middleware.Handler) middleware.Handler {
		if next == nil {
			panic("mailbox: nil handler")
		}
		return func(ctx context.Context, request any) (any, error) {
			var reply any
			err := executor.Call(ctx, func() error {
				var err error
				reply, err = next(ctx, request)
				return err
			})
			return reply, MapError(err)
		}
	}
}

func MapError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.Is(err, ErrFull):
		return kerrors.TooManyRequests("NODE_BUSY", "node request queue is full").WithCause(err)
	case errors.Is(err, ErrNotStarted), errors.Is(err, ErrStopped):
		return kerrors.ServiceUnavailable("NODE_UNAVAILABLE", "node request mailbox is unavailable").WithCause(err)
	default:
		return err
	}
}

func callSafely(job func()) {
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Error("mailbox job panicked", "panic", recovered, "stack", string(debug.Stack()))
		}
	}()
	job()
}
