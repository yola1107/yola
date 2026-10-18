package node

import (
	"context"
	"sync"
	"sync/atomic"

	"yola/internal/contextwait"
)

const (
	_admissionPending uint32 = iota
	_admissionOpen
	_admissionClosed
)

// requestAdmission 统一管理准入阶段、已接纳请求和排空完成信号；零值尚未开放。
type requestAdmission struct {
	state  atomic.Uint32
	mu     sync.Mutex
	active int
	// drained 只在停止时创建，完成后不再接纳请求。
	drained chan struct{}
}

func (a *requestAdmission) open() {
	a.state.CompareAndSwap(_admissionPending, _admissionOpen)
}

func (a *requestAdmission) admit() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state.Load() != _admissionOpen {
		return false
	}
	a.active++
	return true
}

func (a *requestAdmission) done() {
	a.mu.Lock()
	a.active--
	if a.active == 0 && a.drained != nil {
		close(a.drained)
	}
	a.mu.Unlock()
}

func (a *requestAdmission) stopAndWait(ctx context.Context) error {
	a.close()
	a.mu.Lock()
	if a.active == 0 {
		a.mu.Unlock()
		return nil
	}
	if a.drained == nil {
		a.drained = make(chan struct{})
	}
	drained := a.drained
	a.mu.Unlock()
	return contextwait.Done(ctx, drained)
}

func (a *requestAdmission) close() {
	a.state.Store(_admissionClosed)
}

func (a *requestAdmission) isClosed() bool {
	return a.state.Load() == _admissionClosed
}
