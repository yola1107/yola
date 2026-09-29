package network

import (
	"context"
	"errors"

	"yola/api/protocol/v1"
)

var (
	ErrConnectionClosed = errors.New("network: connection closed") // 连接已不能接纳消息
	ErrSendQueueFull    = errors.New("network: send queue full")   // 连接的发送队列无法再接纳消息
	ErrFrameTooLarge    = errors.New("network: frame too large")   // 序列化协议帧超过 transport 大小限制
)

// ConnectionHandlerOperation 标识 transport 对 ConnectionHandler.Handle 的调用。
const ConnectionHandlerOperation = "/network.ConnectionHandler/Handle"

// Connection 是 transport server 持有的一条外部客户端连接。
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

// PreparedConnection 可在 fanout 中共用一次 protobuf 编码。
type PreparedConnection interface {
	Connection
	// SendPrepared 返回前必须结束对 prepared 的全部访问；
	// 可保留 Marshal 返回的不可变 bytes，但不能保留 prepared 或 Message。
	SendPrepared(prepared *PreparedProto) error
}

// ConnectionHandler 处理外部连接的生命周期和解码后消息。
// 连接或服务停止时，Close 可能收到已取消的 context。
// 必须完成清理的 handler 应脱离原取消信号，并设置有限 deadline。
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
