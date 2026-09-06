package node

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"yola/internal/contextwait"
	"yola/locate"

	"github.com/google/uuid"
)

const (
	DefaultNodeEpochTTL    = 30 * time.Second
	nodeEpochRenewInterval = 10 * time.Second
	nodeEpochRPCTimeout    = 3 * time.Second
)

var errNodeEpochExpired = errors.New("node: epoch lease expired")

type epochStore interface {
	RegisterNodeEpoch(context.Context, string, string, string, time.Duration) error
	RenewNodeEpoch(context.Context, string, string, string, time.Duration) error
	UnregisterNodeEpoch(context.Context, string, string, string) error
}

type epochReleaseState uint8

const (
	epochHeld epochReleaseState = iota
	epochReleaseFailed
	epochReleased
)

// epochLease 拥有一次成功申请的固定凭据、续租取消和清理状态，不引用 Server。
type epochLease struct {
	store    epochStore
	identity nodeIdentity
	ctx      context.Context
	cancel   context.CancelCauseFunc
	// deadline 从存储调用开始计时，保留单调时钟；迟到的响应不能恢复失效租约。
	deadline atomic.Pointer[time.Time]

	mu           sync.Mutex
	done         chan struct{}
	releaseState epochReleaseState
}

func claimEpoch(ctx context.Context, store epochStore, identity nodeIdentity) (*epochLease, error) {
	identity.epoch = uuid.NewString()
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, nodeEpochRPCTimeout)
	defer cancel()
	if err := store.RegisterNodeEpoch(ctx, identity.serviceName, identity.nodeID, identity.epoch, DefaultNodeEpochTTL); err != nil {
		return nil, fmt.Errorf("node: register epoch: %w", err)
	}
	lease := newEpochLease(store, identity, started.Add(DefaultNodeEpochTTL))
	// 存储已确认申请成功，即使调用方取消，也须把凭据交给准备 owner 回滚。
	if err := ctx.Err(); err != nil {
		return lease, fmt.Errorf("node: register epoch: %w", err)
	}
	return lease, lease.valid()
}

func newEpochLease(store epochStore, identity nodeIdentity, deadline time.Time) *epochLease {
	ctx, cancel := context.WithCancelCause(context.Background())
	lease := &epochLease{store: store, identity: identity, ctx: ctx, cancel: cancel}
	lease.deadline.Store(&deadline)
	return lease
}

// start 由 Server 保证只调用一次；首次续租完成前不得开放服务。
func (e *epochLease) start(onLost func(error)) <-chan error {
	if e == nil {
		return nil
	}
	ready := make(chan error, 1)
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.valid(); err != nil {
		ready <- err
		return ready
	}
	e.done = make(chan struct{})
	go func() {
		defer close(e.done)
		results := make(chan error, 1)
		renewalDone := make(chan struct{})
		go func() {
			defer close(renewalDone)
			e.renewLoop(results)
		}()
		err := e.monitor(ready, results)
		e.cancel(err)
		if !errors.Is(err, context.Canceled) {
			onLost(err)
		}
		// 先通知失效，再等待 I/O 退出，避免不响应取消的存储拖延 fencing。
		<-renewalDone
	}()
	return ready
}

func (e *epochLease) monitor(ready chan<- error, results <-chan error) error {
	timer := time.NewTimer(time.Until(*e.deadline.Load()))
	defer timer.Stop()
	for {
		select {
		case err := <-results:
			if ready != nil {
				ready <- err
				ready = nil
				if err != nil {
					return err
				}
			}
		case <-timer.C:
		case <-e.ctx.Done():
		}
		if err := e.valid(); err != nil {
			if ready != nil {
				ready <- err
			}
			return err
		}
		timer.Reset(time.Until(*e.deadline.Load()))
	}
}

func (e *epochLease) renewLoop(results chan<- error) {
	ticker := time.NewTicker(nodeEpochRenewInterval)
	defer ticker.Stop()
	for {
		err := e.renewOnce(e.ctx)
		select {
		case results <- err:
		case <-e.ctx.Done():
			return
		}
		select {
		case <-e.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (e *epochLease) valid() error {
	if e == nil {
		return nil
	}
	if !time.Now().Before(*e.deadline.Load()) {
		e.cancel(errNodeEpochExpired)
	}
	return context.Cause(e.ctx)
}

// requestContext 让租约失效取消已接纳工作；正常排空期间租约继续续期。
func (e *epochLease) requestContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if e == nil {
		return ctx, func() {}
	}
	ctx, cancel := context.WithCancelCause(ctx)
	stop := context.AfterFunc(e.ctx, func() { cancel(context.Cause(e.ctx)) })
	if err := e.valid(); err != nil {
		cancel(err)
	}
	return ctx, func() {
		stop()
		cancel(context.Canceled)
	}
}

func (e *epochLease) stopRenewal(ctx context.Context) error {
	if e == nil {
		return nil
	}
	e.cancel(context.Canceled)
	e.mu.Lock()
	done := e.done
	e.mu.Unlock()
	if done == nil {
		return nil
	}
	return contextwait.Done(ctx, done)
}

// renewOnce 只在原租约仍有效且 I/O 按时完成时延长本地有效期。
func (e *epochLease) renewOnce(ctx context.Context) error {
	if err := e.valid(); err != nil {
		return err
	}
	started := time.Now()
	renewalDeadline := *e.deadline.Load()
	if rpcDeadline := started.Add(nodeEpochRPCTimeout); rpcDeadline.Before(renewalDeadline) {
		renewalDeadline = rpcDeadline
	}
	ctx, cancel := context.WithDeadline(ctx, renewalDeadline)
	defer cancel()
	identity := e.identity
	err := e.store.RenewNodeEpoch(ctx, identity.serviceName, identity.nodeID, identity.epoch, DefaultNodeEpochTTL)
	if err == nil && !time.Now().Before(renewalDeadline) {
		err = context.DeadlineExceeded
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = e.valid()
	}
	if err == nil {
		expiresAt := started.Add(DefaultNodeEpochTTL)
		e.deadline.Store(&expiresAt)
		return nil
	}
	slog.WarnContext(ctx, "node epoch renew failed", "node_id", identity.nodeID, "service", identity.serviceName, "error", err)
	if errors.Is(err, locate.ErrNodeEpochNotFound) || errors.Is(err, locate.ErrNodeEpochConflict) {
		err = fmt.Errorf("node: epoch ownership lost: %w", err)
		e.cancel(err)
	}
	return err
}

// release 只能由完成排空的 Server 或准备失败的创建方调用。
func (e *epochLease) release(ctx context.Context) error {
	if e == nil {
		return nil
	}
	stopErr := e.stopRenewal(ctx)
	e.mu.Lock()
	defer e.mu.Unlock()
	if stopErr != nil {
		e.releaseState = epochReleaseFailed
		return stopErr
	}
	return e.releaseLocked(ctx)
}

func (e *epochLease) retryRelease(ctx context.Context) error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.releaseState != epochReleaseFailed {
		return nil
	}
	// 上次可能因续租任务未退出而超时，重试仍须等待它结束。
	if e.done != nil {
		if err := contextwait.Done(ctx, e.done); err != nil {
			return err
		}
	}
	return e.releaseLocked(ctx)
}

func (e *epochLease) releaseLocked(ctx context.Context) error {
	if e.releaseState == epochReleased {
		return nil
	}
	identity := e.identity
	err := e.store.UnregisterNodeEpoch(ctx, identity.serviceName, identity.nodeID, identity.epoch)
	if err != nil && !errors.Is(err, locate.ErrNodeEpochNotFound) && !errors.Is(err, locate.ErrNodeEpochConflict) {
		e.releaseState = epochReleaseFailed
		return err
	}
	e.releaseState = epochReleased
	return nil
}
