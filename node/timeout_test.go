package node

import (
	"context"
	"errors"
	"testing"
	"time"

	"yola/locate"

	"github.com/stretchr/testify/require"
)

func TestPushTimeoutDoesNotCancelStartupRollback(t *testing.T) {
	called := false
	var remaining time.Duration
	server := newTestServer(t, PushTimeout(time.Nanosecond), Drain(func(ctx context.Context) error {
		called = true
		deadline, ok := ctx.Deadline()
		if ok {
			remaining = time.Until(deadline)
		}
		return ctx.Err()
	}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, server.rollbackStart(ctx))
	require.True(t, called, "startup rollback must reach business Drain with its own budget")
	require.Greater(t, remaining, time.Second)
}

func TestPushTimeoutDoesNotCancelEpochRollback(t *testing.T) {
	base := newMemoryLocator()
	store := &contextCheckedUnregisterLocator{Locator: base}
	server := newTestServer(t, Locator(store), PushTimeout(time.Nanosecond))
	t.Cleanup(func() { require.NoError(t, server.Stop(context.Background())) })
	identity := nodeIdentity{serviceName: "game", nodeID: "node-a", epoch: "epoch-a"}
	require.NoError(t, base.RegisterNodeEpoch(context.Background(), identity.serviceName, identity.nodeID, identity.epoch, DefaultNodeEpochTTL))
	lease := newEpochLease(store, identity, time.Now().Add(DefaultNodeEpochTTL))
	cause := errors.New("preparation failed")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := server.rollbackPreparation(ctx, lease, cause)
	require.ErrorIs(t, err, cause)
	require.NotErrorIs(t, err, context.DeadlineExceeded)
	require.Greater(t, store.remaining, time.Second)
	_, err = base.LocateNodeEpoch(context.Background(), identity.serviceName, identity.nodeID)
	require.ErrorIs(t, err, locate.ErrNodeEpochNotFound)
}

func TestCleanupTimeoutBoundsStartupRollback(t *testing.T) {
	server := newTestServer(t, PushTimeout(time.Minute), CleanupTimeout(20*time.Millisecond), Drain(func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}))
	require.ErrorIs(t, server.rollbackStart(context.Background()), context.DeadlineExceeded)
}

func TestNormalStopKeepsCallerBudget(t *testing.T) {
	called := false
	server := newTestServer(t, CleanupTimeout(time.Nanosecond), Drain(func(ctx context.Context) error {
		called = true
		return ctx.Err()
	}))
	require.NoError(t, server.Stop(context.Background()))
	require.True(t, called)
}

type contextCheckedUnregisterLocator struct {
	locate.Locator
	remaining time.Duration
}

func (l *contextCheckedUnregisterLocator) UnregisterNodeEpoch(ctx context.Context, service, nodeID, epoch string) error {
	if deadline, ok := ctx.Deadline(); ok {
		l.remaining = time.Until(deadline)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return l.Locator.UnregisterNodeEpoch(ctx, service, nodeID, epoch)
}
