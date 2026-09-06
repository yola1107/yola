package node

import (
	"context"

	"github.com/go-kratos/kratos/v3/errors"
	"github.com/go-kratos/kratos/v3/middleware"
	"google.golang.org/protobuf/proto"
)

type requestPointer[Request any] interface {
	*Request
	proto.Message
}

// Register adapts a protobuf handler to the Node command transport.
func Register[
	Command ~int32,
	Request any,
	RequestPtr requestPointer[Request],
	Reply proto.Message,
](server *Server, command Command, handler func(context.Context, RequestPtr) (Reply, error)) {

	if server == nil {
		panic("node: nil server")
	}
	server.RegisterRawHandler(int32(command), unary(server.middlewares, handler))
}

func unary[
	Request any,
	RequestPtr requestPointer[Request],
	Reply proto.Message,
](middlewares []middleware.Middleware, handler func(context.Context, RequestPtr) (Reply, error)) Handler {

	if handler == nil {
		panic("node: nil unary handler")
	}
	next := middleware.Chain(middlewares...)(func(ctx context.Context, request any) (any, error) {
		return handler(ctx, request.(RequestPtr))
	})
	return func(ctx context.Context, body []byte) ([]byte, error) {
		req := RequestPtr(new(Request))
		if err := proto.Unmarshal(body, req); err != nil {
			return nil, errors.BadRequest("INVALID_REQUEST", "decode request").WithCause(err)
		}
		reply, err := next(ctx, req)
		if err != nil {
			return nil, err
		}
		message, ok := reply.(proto.Message)
		if !ok || !validMessage(message) {
			return nil, errors.InternalServer("INVALID_REPLY", "invalid reply")
		}
		body, err = proto.Marshal(message)
		if err != nil {
			return nil, errors.InternalServer("INVALID_REPLY", "encode reply").WithCause(err)
		}
		return body, nil
	}
}

func validMessage(msg proto.Message) bool {
	return msg != nil && msg.ProtoReflect().IsValid()
}
