package biz

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"yola/locate"
	"yola/test/whot/pkg/codes"

	"github.com/stretchr/testify/require"
)

func TestRenewConflictNeverRebindsResidentPlayer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		uc, cleanup, err := NewUsecase(new(usecaseRepoStub), whotDrainTestRoom())
		require.NoError(t, err)
		t.Cleanup(cleanup)
		sess := &residencySession{
			connectionSession: connectionSession{uid: "42", bindingToken: "resident"},
			renew:             func(context.Context) error { return locate.ErrNodeConflict },
		}
		reply, err := uc.Login(t.Context(), sess, 42, "valid")
		require.NoError(t, err)
		require.Equal(t, codes.Success, reply.Code)
		synctest.Wait()

		time.Sleep(4 * time.Minute)
		synctest.Wait()

		require.Equal(t, int64(2), sess.renews.Load())
		require.Equal(t, int64(1), sess.binds.Load())
	})
}

func TestResidencyFollowsReconnectAndStopsAfterLogout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		uc, cleanup, err := NewUsecase(new(usecaseRepoStub), whotDrainTestRoom())
		require.NoError(t, err)
		t.Cleanup(cleanup)
		first := &residencySession{connectionSession: connectionSession{uid: "42", bindingToken: "first"}}
		reply, err := uc.Login(t.Context(), first, 42, "valid")
		require.NoError(t, err)
		require.Equal(t, codes.Success, reply.Code)
		synctest.Wait()
		time.Sleep(2 * time.Minute)
		synctest.Wait()

		current := &residencySession{connectionSession: connectionSession{uid: "42", bindingToken: "current"}}
		reply, err = uc.Login(t.Context(), current, 42, "valid")
		require.NoError(t, err)
		require.Equal(t, codes.Success, reply.Code)
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		require.Equal(t, int64(1), first.renews.Load())
		require.Equal(t, int64(1), current.renews.Load())

		loggedOut, err := uc.Logout(t.Context(), 42)
		require.NoError(t, err)
		require.Equal(t, codes.Success, loggedOut.Code)
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		require.Equal(t, int64(1), current.renews.Load())
	})
}

func TestDrainCancelsAndWaitsForNodeRenewal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		uc, cleanup, err := NewUsecase(new(usecaseRepoStub), whotDrainTestRoom())
		require.NoError(t, err)
		release := make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		t.Cleanup(func() {
			unblock()
			cleanup()
		})
		budgets := make(chan time.Duration, 1)
		canceled := make(chan error, 1)
		sess := &residencySession{
			connectionSession: connectionSession{uid: "42", bindingToken: "resident"},
			renew: func(ctx context.Context) error {
				deadline, _ := ctx.Deadline()
				budgets <- time.Until(deadline)
				<-ctx.Done()
				canceled <- ctx.Err()
				<-release
				return ctx.Err()
			},
		}
		reply, err := uc.Login(t.Context(), sess, 42, "valid")
		require.NoError(t, err)
		require.Equal(t, codes.Success, reply.Code)
		synctest.Wait()
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		require.Equal(t, int64(1), sess.renews.Load())
		require.Equal(t, 3*time.Second, <-budgets)
		drained := make(chan error, 1)
		go func() { drained <- uc.Drain(t.Context()) }()
		synctest.Wait()
		require.ErrorIs(t, <-canceled, context.Canceled)
		select {
		case err := <-drained:
			t.Fatalf("Drain returned before the renewal exited: %v", err)
		default:
		}
		unblock()
		require.NoError(t, <-drained)
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		require.Equal(t, int64(1), sess.renews.Load())
	})
}

func TestResidentPlayerRenewsNodeWithoutRebinding(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		uc, cleanup, err := NewUsecase(new(usecaseRepoStub), whotDrainTestRoom())
		require.NoError(t, err)
		t.Cleanup(cleanup)
		sess := &residencySession{connectionSession: connectionSession{uid: "42", bindingToken: "resident"}}
		reply, err := uc.Login(t.Context(), sess, 42, "valid")
		require.NoError(t, err)
		require.Equal(t, codes.Success, reply.Code)
		synctest.Wait()

		time.Sleep(2 * time.Minute)
		synctest.Wait()

		require.Equal(t, int64(1), sess.renews.Load())
		require.Equal(t, int64(1), sess.binds.Load())
	})
}

type residencySession struct {
	connectionSession
	binds  atomic.Int64
	renews atomic.Int64
	renew  func(context.Context) error
}

func (sess *residencySession) BindNode(ctx context.Context) error {
	sess.binds.Add(1)
	return sess.connectionSession.BindNode(ctx)
}

func (sess *residencySession) RenewNode(ctx context.Context) error {
	sess.renews.Add(1)
	if sess.renew != nil {
		return sess.renew(ctx)
	}
	return nil
}
