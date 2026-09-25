package network

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
