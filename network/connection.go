package network

import (
	"context"
	"errors"

	v1 "yola/api/protocol/v1"
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

// SendStats 是单连接发送侧的只读快照，各字段不保证同一时刻采样。
// PendingPayloadBytes 累加 Proto.Body 长度，包含等待入队的发送者和排队帧，
// 不含 writer 已取走的帧、编码及 socket 开销；共享 Payload 按发送次数计数，不代表 RSS。
// 关闭后未写出的排队帧仍计入快照，QueueDropped 累计值不重置。
type SendStats struct {
	QueueDepth          int
	QueueCapacity       int
	PendingPayloadBytes int64
	QueueDropped        uint64 // 仅统计发送队列满的拒绝，不含关闭、编码或写出错误。
	Closed              bool   // 已关闭新发送准入，不表示 writer 已退出。
}

// SendStatsProvider 是 Connection 可选的只读发送观测能力。
type SendStatsProvider interface {
	SendStats() SendStats
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
