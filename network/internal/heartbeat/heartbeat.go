// Package heartbeat owns the concurrent state of one client heartbeat.
package heartbeat

import "sync/atomic"

// TickResult tells a transport what to do when its heartbeat ticker fires.
type TickResult uint8

const (
	// TickQueue asks the transport to enqueue a heartbeat frame.
	TickQueue TickResult = iota
	// TickPending reports that the previous heartbeat has not been written yet.
	TickPending
	// TickTimeout reports that a written heartbeat was not acknowledged.
	TickTimeout
)

const (
	idle uint32 = iota
	queued
	writing
	outstanding
)

// State owns the atomic transitions for one queued or outstanding heartbeat.
// Its zero value is ready for use.
type State struct {
	value atomic.Uint32
}

// Tick advances an idle heartbeat to queued, keeps an unwritten heartbeat
// pending, or consumes an outstanding heartbeat as timed out.
func (s *State) Tick() TickResult {
	for {
		switch state := s.value.Load(); state {
		case idle:
			if s.value.CompareAndSwap(idle, queued) {
				return TickQueue
			}
		case queued, writing:
			return TickPending
		case outstanding:
			if s.value.CompareAndSwap(outstanding, idle) {
				return TickTimeout
			}
		}
	}
}

// CancelQueue returns a heartbeat that could not be enqueued to idle.
func (s *State) CancelQueue() bool {
	return s.value.CompareAndSwap(queued, idle)
}

// BeginWrite marks a queued heartbeat as being written.
func (s *State) BeginWrite() bool {
	return s.value.CompareAndSwap(queued, writing)
}

// FinishWrite marks a completed heartbeat write as awaiting its reply. A reply
// may have already moved writing to idle, and a ticker may have queued the next
// generation, before the old write call returned.
func (s *State) FinishWrite() bool {
	if s.value.CompareAndSwap(writing, outstanding) {
		return true
	}
	state := s.value.Load()
	return state == idle || state == queued
}

// Reply acknowledges a heartbeat whose write is in progress or complete.
func (s *State) Reply() bool {
	for {
		state := s.value.Load()
		if state != writing && state != outstanding {
			return false
		}
		if s.value.CompareAndSwap(state, idle) {
			return true
		}
	}
}
