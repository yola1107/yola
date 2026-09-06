package network

import (
	"context"
	"testing"
	"time"

	"yola/api/protocol/v1"

	"github.com/go-kratos/kratos/v3/middleware"
)

type invokeConnection struct{}

func (invokeConnection) ConnID() string            { return "conn-a" }
func (invokeConnection) RemoteAddr() string        { return "127.0.0.1:5000" }
func (invokeConnection) SendProto(*v1.Proto) error { return nil }
func (invokeConnection) CloseWithProto(context.Context, *v1.Proto) error {
	return nil
}
func (invokeConnection) Close() error { return nil }

type invokeHandler struct{}

func (invokeHandler) Open(context.Context, Connection) error { return nil }
func (invokeHandler) Handle(_ context.Context, _ Connection, message *v1.Proto) (*v1.Proto, error) {
	return message, nil
}
func (invokeHandler) Close(context.Context, Connection) {}

func TestConnectionHandlerOperation(t *testing.T) {
	if ConnectionHandlerOperation != "/network.ConnectionHandler/Handle" {
		t.Fatalf("ConnectionHandlerOperation = %q", ConnectionHandlerOperation)
	}
}

func TestInvokerBuildsMiddlewareOnce(t *testing.T) {
	built := 0
	called := 0
	m := func(next middleware.Handler) middleware.Handler {
		built++
		return func(ctx context.Context, req any) (any, error) {
			called++
			reply, err := next(ctx, req)
			if err == nil {
				reply.(*v1.Proto).Code = 7
			}
			return reply, err
		}
	}
	invoke := NewInvoker(invokeHandler{}, invokeConnection{}, 0, m)
	for range 2 {
		reply, err := invoke(context.Background(), &v1.Proto{})
		if err != nil {
			t.Fatal(err)
		}
		if reply.Code != 7 {
			t.Fatalf("reply code = %d, want 7", reply.Code)
		}
	}
	if built != 1 || called != 2 {
		t.Fatalf("middleware built = %d, called = %d, want 1 and 2", built, called)
	}
}

func TestInvokerAppliesHandlerTimeout(t *testing.T) {
	handler := connectionHandlerFunc(func(ctx context.Context, _ Connection, _ *v1.Proto) (*v1.Proto, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	invoke := NewInvoker(handler, invokeConnection{}, time.Millisecond)
	_, err := invoke(context.Background(), &v1.Proto{})
	if err != context.DeadlineExceeded {
		t.Fatalf("Invoke() error = %v, want deadline exceeded", err)
	}
}

type connectionHandlerFunc func(context.Context, Connection, *v1.Proto) (*v1.Proto, error)

func (h connectionHandlerFunc) Open(context.Context, Connection) error { return nil }
func (h connectionHandlerFunc) Close(context.Context, Connection)      {}
func (h connectionHandlerFunc) Handle(ctx context.Context, conn Connection, message *v1.Proto) (*v1.Proto, error) {
	return h(ctx, conn, message)
}
