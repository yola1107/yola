package network

import (
	"context"
	"errors"
	"time"

	"yola/api/protocol/v1"

	"github.com/go-kratos/kratos/v3/middleware"
)

var errInvalidHandlerReply = errors.New("network: handler returned an invalid reply")

// Invoker runs decoded messages for one connection through a fixed middleware chain.
type Invoker func(context.Context, *v1.Proto) (*v1.Proto, error)

// NewInvoker builds the middleware chain once for a connection.
func NewInvoker(handler ConnectionHandler, conn Connection, timeout time.Duration, middlewares ...middleware.Middleware) Invoker {
	h := middleware.Chain(middlewares...)(func(ctx context.Context, req any) (any, error) {
		message, ok := req.(*v1.Proto)
		if !ok || message == nil {
			return nil, errInvalidHandlerReply
		}
		return handler.Handle(ctx, conn, message)
	})
	return func(ctx context.Context, message *v1.Proto) (*v1.Proto, error) {
		if timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}
		reply, err := h(ctx, message)
		if err != nil {
			return nil, err
		}
		p, ok := reply.(*v1.Proto)
		if !ok || p == nil {
			return nil, errInvalidHandlerReply
		}
		return p, nil
	}
}
