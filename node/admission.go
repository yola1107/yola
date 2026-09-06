package node

import (
	"context"
	"sync"
	"sync/atomic"

	"yola/internal/contextwait"
)

// requestAdmission 统一管理终态、已接纳请求和排空完成信号；零值允许接纳请求。
type requestAdmission struct {
	closed atomic.Bool
	mu     sync.Mutex
	active int
	// drained 只在停止时创建，完成后不再接纳请求。
	drained chan struct{}
}

func (a *requestAdmission) admit() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.isClosed() {
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

func (a *requestAdmission) close() {
	a.closed.Store(true)
}

func (a *requestAdmission) isClosed() bool {
	return a.closed.Load()
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
