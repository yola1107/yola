package event

import "time"

// SubscriptionStats 是单个订阅的只读快照，累计值不因退订而重置。
// QueueDropped 只统计 adapter 本地接收队列拒绝，不表示 broker 或断线期间的丢失。
type SubscriptionStats struct {
	QueueDepth          int
	QueueCapacity       int
	QueueDropped        uint64
	QueueDroppedCurrent bool // false 时只保留最后可得值，不能视为精确最终值。
	PayloadDropped      uint64
	HandlerCalls        uint64 // 已结束的调用，包含 panic。
	HandlerPanics       uint64
	HandlerActive       bool
	HandlerDuration     time.Duration
	LastHandlerDuration time.Duration
	MaxHandlerDuration  time.Duration
	Closed              bool // 消费协程已退出。
}

// SubscriptionStatsProvider 是 adapter 可选的观测能力，不暴露底层队列或连接。
type SubscriptionStatsProvider interface {
	SubscriptionStats() SubscriptionStats
}
