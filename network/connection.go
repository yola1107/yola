package network

import (
	"context"
	"errors"

	"yola/api/protocol/v1"
)

var (
	// ErrConnectionClosed indicates that a connection can no longer accept messages.
	ErrConnectionClosed = errors.New("network: connection closed")
	// ErrSendQueueFull indicates that a connection cannot accept another queued message.
	ErrSendQueueFull = errors.New("network: send queue full")
	// ErrFrameTooLarge indicates that a serialized protocol frame exceeds the transport limit.
	ErrFrameTooLarge = errors.New("network: frame too large")
)

// ConnectionHandlerOperation identifies transport calls into ConnectionHandler.Handle.
const ConnectionHandlerOperation = "/network.ConnectionHandler/Handle"

// Connection is one external client connection owned by a transport server.
type Connection interface {
	ConnID() string
	RemoteAddr() string
	// SendProto 接纳一个不可变消息。调用期间及成功返回后，调用方不得修改
	// Proto、Body 或其他可变字段的任何别名；可将同一不可变消息发给多个连接。
	// 返回成功不表示已经编码或写入 socket；需修改或复用时先创建独立消息及数据。
	SendProto(*v1.Proto) error
	CloseWithProto(context.Context, *v1.Proto) error
	Close() error
}

// PreparedConnection can share one protobuf encoding across a fanout.
type PreparedConnection interface {
	Connection
	// SendPrepared must finish all access to prepared before returning. It may
	// retain the immutable bytes returned by Marshal, but not prepared or Message.
	SendPrepared(prepared *PreparedProto) error
}

// ConnectionHandler handles external connection lifecycle and decoded messages.
// Close may receive a canceled context when the connection or server is shutting down.
// Handlers that must finish cleanup should detach cancellation and apply a finite deadline.
type ConnectionHandler interface {
	Open(ctx context.Context, conn Connection) error
	Close(ctx context.Context, conn Connection)
	Handle(ctx context.Context, conn Connection, message *v1.Proto) (*v1.Proto, error)
}

// HeartbeatHandler 允许认证后并发处理心跳与业务请求。
// Heartbeat 不与 Close 并发；实现者及 middleware 须保护心跳和 Handle 共享的状态。
type HeartbeatHandler interface {
	Heartbeat(context.Context, Connection) error
}
