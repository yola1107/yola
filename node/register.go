package node

import (
	"context"
	"fmt"

	"github.com/go-kratos/kratos/v3/errors"
	"github.com/go-kratos/kratos/v3/middleware"
	"google.golang.org/protobuf/proto"
)

// Handler 处理原始 command body；可通过 FromContext 和 CommandFromContext 读取本次请求信息。
type Handler func(context.Context, []byte) ([]byte, error)

type requestPointer[Request any] interface {
	*Request
	proto.Message
}

// Register 将 protobuf handler 适配为 Node command handler。
func Register[Command ~int32, Request any, RequestPtr requestPointer[Request], Reply proto.Message](
	server *Server, command Command, handler func(context.Context, RequestPtr) (Reply, error),
) {

	if server == nil {
		panic("node: nil server")
	}
	server.RegisterRawHandler(int32(command), unary(server.middlewares, handler))
}

// RegisterRawHandler 注册自定义 codec 或装饰后的 handler，不应用 node.Middleware。
// 必须在 BeforeStart 前调用。
func (s *Server) RegisterRawHandler(command int32, handler Handler) {
	if handler == nil {
		panic("node: nil handler")
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.state != stNew || s.requests.isClosed() {
		panic("node: handlers must be registered before BeforeStart")
	}
	if _, exists := s.handlers[command]; exists {
		panic(fmt.Sprintf("node: duplicate Command=%d", command))
	}
	s.handlers[command] = handler
}

// OnDisconnect 在 BeforeStart 前注册 best-effort 断线 handler；nil 清除注册。
// 事件至多投递一次，与 Forward 的完成顺序无保证。
func (s *Server) OnDisconnect(handler DisconnectHandler) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.state != stNew || s.requests.isClosed() {
		panic("node: handlers must be registered before BeforeStart")
	}
	s.onDisconnect = handler
}

func unary[Request any, RequestPtr requestPointer[Request], Reply proto.Message](
	middlewares []middleware.Middleware, handler func(context.Context, RequestPtr) (Reply, error),
) Handler {

	if handler == nil {
		panic("node: nil unary handler")
	}
	next := middleware.Chain(middlewares...)(func(ctx context.Context, request any) (any, error) {
		return handler(ctx, request.(RequestPtr))
	})
	return func(ctx context.Context, body []byte) ([]byte, error) {
		request := RequestPtr(new(Request))
		if err := proto.Unmarshal(body, request); err != nil {
			return nil, errors.BadRequest("INVALID_REQUEST", "decode request").WithCause(err)
		}
		reply, err := next(ctx, request)
		if err != nil {
			return nil, err
		}
		message, ok := reply.(proto.Message)
		if !ok || !validMessage(message) {
			return nil, errors.InternalServer("INVALID_REPLY", "invalid reply")
		}
		replyBody, err := proto.Marshal(message)
		if err != nil {
			return nil, errors.InternalServer("INVALID_REPLY", "encode reply").WithCause(err)
		}
		return replyBody, nil
	}
}

func validMessage(msg proto.Message) bool {
	return msg != nil && msg.ProtoReflect().IsValid()
}
