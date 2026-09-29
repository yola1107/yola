// Package heartbeat 持有单个客户端心跳的并发状态。
package heartbeat

import "sync/atomic"

// TickResult 表示心跳定时器触发时 transport 应执行的动作。
type TickResult uint8

const (
	TickQueue   TickResult = iota // 要求 transport 将心跳帧入队
	TickPending                   // 上次心跳尚未写出
	TickTimeout                   // 已写出的心跳未获确认
)

const (
	_idle uint32 = iota
	_queued
	_writing
	_outstanding
)

// State 持有一代排队中或等待回复的心跳，并原子切换状态。
// 零值可直接使用。
type State struct {
	value atomic.Uint32
}

// Tick 将空闲心跳转为排队，保持尚未写出的心跳为 pending，
// 或将等待回复的心跳判为超时并消费其状态。
func (s *State) Tick() TickResult {
	for {
		switch state := s.value.Load(); state {
		case _idle:
			if s.value.CompareAndSwap(_idle, _queued) {
				return TickQueue
			}
		case _queued, _writing:
			return TickPending
		case _outstanding:
			if s.value.CompareAndSwap(_outstanding, _idle) {
				return TickTimeout
			}
		}
	}
}

// CancelQueue 将无法入队的心跳恢复为空闲。
func (s *State) CancelQueue() bool {
	return s.value.CompareAndSwap(_queued, _idle)
}

// BeginWrite 将排队心跳标为正在写入。
func (s *State) BeginWrite() bool {
	return s.value.CompareAndSwap(_queued, _writing)
}

// FinishWrite 将写完的心跳标为等待回复。
// 旧写调用返回前，回复可能已将 writing 转为空闲，
// 定时器也可能已排入下一代心跳。
func (s *State) FinishWrite() bool {
	if s.value.CompareAndSwap(_writing, _outstanding) {
		return true
	}
	state := s.value.Load()
	return state == _idle || state == _queued
}

// Reply 确认正在写入或已写完的心跳。
func (s *State) Reply() bool {
	for {
		state := s.value.Load()
		if state != _writing && state != _outstanding {
			return false
		}
		if s.value.CompareAndSwap(state, _idle) {
			return true
		}
	}
}
