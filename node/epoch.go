package node

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"yola/locate"

	"github.com/google/uuid"
)

const (
	DefaultNodeEpochTTL    = 30 * time.Second
	nodeEpochRenewInterval = 10 * time.Second
)

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
	cancel   context.CancelFunc

	mu           sync.Mutex
	releaseState epochReleaseState
}

func claimEpoch(ctx context.Context, store epochStore, identity nodeIdentity) (*epochLease, error) {
	identity.epoch = uuid.NewString()
	if err := store.RegisterNodeEpoch(ctx, identity.serviceName, identity.nodeID, identity.epoch, DefaultNodeEpochTTL); err != nil {
		return nil, fmt.Errorf("node: register epoch: %w", err)
	}
	return newEpochLease(store, identity), nil
}

func newEpochLease(store epochStore, identity nodeIdentity) *epochLease {
	ctx, cancel := context.WithCancel(context.Background())
	return &epochLease{store: store, identity: identity, ctx: ctx, cancel: cancel}
}

// start 由 Server 的启动状态保证只调用一次，失去租约时仅通知生命周期 owner。
func (e *epochLease) start(onLost func(error)) {
	if e == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(nodeEpochRenewInterval)
		defer ticker.Stop()
		for {
			select {
			case <-e.ctx.Done():
				return
			case <-ticker.C:
				if !e.renewOnce(e.ctx, onLost) {
					return
				}
			}
		}
	}()
}

func (e *epochLease) stopRenewal() {
	if e != nil {
		e.cancel()
	}
}

// renewOnce 保留普通存储错误的重试语义；明确丢失 epoch 才终止服务。
func (e *epochLease) renewOnce(ctx context.Context, onLost func(error)) bool {
	identity := e.identity
	err := e.store.RenewNodeEpoch(ctx, identity.serviceName, identity.nodeID, identity.epoch, DefaultNodeEpochTTL)
	if err == nil {
		return true
	}
	slog.WarnContext(ctx, "node epoch renew failed", "node_id", identity.nodeID, "service", identity.serviceName, "error", err)
	if !errors.Is(err, locate.ErrNodeEpochNotFound) && !errors.Is(err, locate.ErrNodeEpochConflict) {
		return true
	}
	onLost(fmt.Errorf("node: epoch ownership lost: %w", err))
	return false
}

// release 只能由完成排空的 Server 或准备失败的创建方调用。
func (e *epochLease) release(ctx context.Context) error {
	if e == nil {
		return nil
	}
	e.stopRenewal()
	e.mu.Lock()
	defer e.mu.Unlock()
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
