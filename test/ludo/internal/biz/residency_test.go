package biz

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	v1 "yola/test/ludo/api/v1"
	"yola/test/ludo/internal/biz/table"
	"yola/test/ludo/pkg/codes"

	"github.com/stretchr/testify/require"
	grpccodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestRenewConflictStopsResidentPlayer(t *testing.T) {
	for _, tc := range []struct {
		name   string
		gaming bool
	}{
		{name: "idle"},
		{name: "gaming", gaming: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				repo := new(memoryPlayerRepo)
				uc, cleanup, err := NewUsecase("residency-test", repo, testRoom(1, 4))
				require.NoError(t, err)
				t.Cleanup(cleanup)
				pusher := new(residencyPusher)
				uc.SetClientPusher(pusher)
				sess := &residencySession{
					recordingSession: recordingSession{uid: "42", bindingToken: "resident"},
					renew:            func(context.Context) error { return status.Error(grpccodes.Aborted, "node binding changed") },
				}
				reply, err := uc.Login(t.Context(), sess, 42, "valid", 1)
				require.NoError(t, err)
				require.Equal(t, codes.Success, reply.Code)
				other := &recordingSession{uid: "43", bindingToken: "other"}
				reply, err = uc.Login(t.Context(), other, 43, "valid", 1)
				require.NoError(t, err)
				require.Equal(t, codes.Success, reply.Code)
				p := uc.pm.GetByID(42)
				if tc.gaming {
					require.NoError(t, uc.tm.CallPlayer(t.Context(), p, func(*table.Table) error {
						p.SetGaming()
						return nil
					}))
				}
				pusher.reset()

				uc.renewNodeBindings(t.Context())
				require.Nil(t, p.GetSession())
				if tc.gaming {
					require.True(t, p.IsOffline())
					require.Same(t, p, uc.pm.GetByID(42))
				} else {
					require.Nil(t, uc.pm.GetByID(42))
				}
				_, err = uc.Forward(t.Context(), 43, &v1.ForwardReq{Type: 1, Msg: "old table event"})
				require.NoError(t, err)
				require.False(t, pusher.sentTo("42"))
				uc.renewNodeBindings(t.Context())
				require.Equal(t, int64(1), sess.renews.Load())
				binds, unbinds := sess.counts()
				require.Equal(t, 1, binds)
				require.Zero(t, unbinds)
				require.Zero(t, repo.saved(), "lost ownership must not persist stale player state")
				require.NoError(t, uc.Drain(t.Context()))
				require.Equal(t, 1, repo.saved(), "only the other player still owns its state")
			})
		})
	}
}

func TestTransientRenewalFailureRetainsPlayer(t *testing.T) {
	for _, code := range []grpccodes.Code{grpccodes.Unavailable, grpccodes.DeadlineExceeded} {
		t.Run(code.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				repo := new(memoryPlayerRepo)
				uc, cleanup, err := NewUsecase("residency-test", repo, testRoom(1, 4))
				require.NoError(t, err)
				t.Cleanup(cleanup)
				sess := &residencySession{
					recordingSession: recordingSession{uid: "42", bindingToken: "resident"},
					renew:            func(context.Context) error { return status.Error(code, "temporary failure") },
				}
				reply, err := uc.Login(t.Context(), sess, 42, "valid", 1)
				require.NoError(t, err)
				require.Equal(t, codes.Success, reply.Code)
				uc.renewNodeBindings(t.Context())
				uc.renewNodeBindings(t.Context())
				require.Equal(t, int64(2), sess.renews.Load())
				require.Same(t, sess, uc.pm.GetByID(42).GetSession())
				require.False(t, uc.pm.GetByID(42).IsOffline())
				binds, unbinds := sess.counts()
				require.Equal(t, 1, binds)
				require.Zero(t, unbinds)
			})
		})
	}
}

func TestDelayedRenewConflictPreservesReconnectedPlayer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		repo := new(memoryPlayerRepo)
		uc, cleanup, err := NewUsecase("residency-test", repo, testRoom(1, 4))
		require.NoError(t, err)
		release := make(chan struct{})
		resume := sync.OnceFunc(func() { close(release) })
		done := make(chan struct{})
		t.Cleanup(func() {
			resume()
			cleanup()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Error("renewal did not exit")
			}
		})
		entered := make(chan struct{})
		sess := &residencySession{
			recordingSession: recordingSession{uid: "42", bindingToken: "same-connection"},
			renew: func(context.Context) error {
				close(entered)
				<-release
				return status.Error(grpccodes.Aborted, "old renewal lost ownership")
			},
		}
		reply, err := uc.Login(t.Context(), sess, 42, "valid", 1)
		require.NoError(t, err)
		require.Equal(t, codes.Success, reply.Code)
		go func() {
			uc.renewNodeBindings(t.Context())
			close(done)
		}()
		<-entered
		current := &residencySession{recordingSession: recordingSession{uid: "42", bindingToken: "same-connection"}}
		reply, err = uc.Login(t.Context(), current, 42, "valid", 1)
		require.NoError(t, err)
		require.Equal(t, codes.Success, reply.Code)
		resume()
		<-done
		require.Same(t, current, uc.pm.GetByID(42).GetSession())
		require.False(t, uc.pm.GetByID(42).IsOffline())
		uc.renewNodeBindings(t.Context())
		require.Equal(t, int64(1), current.renews.Load())
	})
}

func TestQueuedBindingLossDoesNotOrphanReconnect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		repo := new(memoryPlayerRepo)
		uc, cleanup, err := NewUsecase("residency-test", repo, testRoom(1, 4))
		require.NoError(t, err)
		t.Cleanup(cleanup)
		sess := &residencySession{
			recordingSession: recordingSession{uid: "42", bindingToken: "old"},
			renew:            func(context.Context) error { return status.Error(grpccodes.Aborted, "node binding changed") },
		}
		reply, err := uc.Login(t.Context(), sess, 42, "valid", 1)
		require.NoError(t, err)
		require.Equal(t, codes.Success, reply.Code)
		old := uc.pm.GetByID(42)
		release := make(chan struct{})
		resume := sync.OnceFunc(func() { close(release) })
		t.Cleanup(resume)
		blocked := make(chan error, 1)
		go func() {
			blocked <- uc.tm.CallPlayer(t.Context(), old, func(*table.Table) error {
				<-release
				return nil
			})
		}()
		synctest.Wait()
		renewed := make(chan struct{})
		go func() {
			uc.renewNodeBindings(t.Context())
			close(renewed)
		}()
		synctest.Wait()
		current := &residencySession{recordingSession: recordingSession{uid: "42", bindingToken: "new"}}
		var loginErr error
		var reconnected *v1.LoginRsp
		loggedIn := make(chan struct{})
		go func() {
			reconnected, loginErr = uc.Login(t.Context(), current, 42, "valid", 1)
			close(loggedIn)
		}()
		t.Cleanup(func() {
			resume()
			for _, done := range []<-chan struct{}{renewed, loggedIn} {
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Error("residency operation did not exit")
				}
			}
		})
		synctest.Wait()
		resume()
		require.NoError(t, <-blocked)
		<-renewed
		<-loggedIn
		require.NoError(t, loginErr)
		require.Equal(t, codes.Success, reconnected.Code)
		require.NotSame(t, old, uc.pm.GetByID(42))
		require.Same(t, current, uc.pm.GetByID(42).GetSession())
		require.False(t, uc.pm.GetByID(42).IsOffline())
		require.Zero(t, repo.saved())
	})
}

func TestFailedReconnectKeepsLostPlayerQuiescent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		repo := new(memoryPlayerRepo)
		uc, cleanup, err := NewUsecase("residency-test", repo, testRoom(1, 4))
		require.NoError(t, err)
		t.Cleanup(cleanup)
		sess := &residencySession{
			recordingSession: recordingSession{uid: "42", bindingToken: "old"},
			renew:            func(context.Context) error { return status.Error(grpccodes.Aborted, "node binding changed") },
		}
		reply, err := uc.Login(t.Context(), sess, 42, "valid", 1)
		require.NoError(t, err)
		require.Equal(t, codes.Success, reply.Code)
		p := uc.pm.GetByID(42)
		require.NoError(t, uc.tm.CallPlayer(t.Context(), p, func(*table.Table) error {
			p.SetGaming()
			return nil
		}))
		uc.renewNodeBindings(t.Context())
		candidate := &recordingSession{uid: "42", bindingToken: "failed", pushErr: status.Error(grpccodes.NotFound, "connection lost")}
		_, err = uc.Login(t.Context(), candidate, 42, "valid", 1)
		require.Error(t, err)
		require.Nil(t, p.GetSession())
		require.True(t, p.IsOffline())
		require.True(t, p.NodeBindingLost())
		_, unbinds := candidate.counts()
		require.Equal(t, 1, unbinds)
		uc.renewNodeBindings(t.Context())
		require.Equal(t, int64(1), sess.renews.Load())
		require.NoError(t, uc.Drain(t.Context()))
		require.Zero(t, repo.saved())
	})
}

func TestResidencyFollowsReconnectAndStopsAfterLogout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		uc, cleanup, err := NewUsecase("ludo-test", new(memoryPlayerRepo), testRoom(1, 4))
		require.NoError(t, err)
		t.Cleanup(cleanup)
		first := &residencySession{recordingSession: recordingSession{uid: "42", bindingToken: "first"}}
		reply, err := uc.Login(t.Context(), first, 42, "valid", 0)
		require.NoError(t, err)
		require.Equal(t, codes.Success, reply.Code)
		synctest.Wait()
		time.Sleep(time.Minute)
		synctest.Wait()

		current := &residencySession{recordingSession: recordingSession{uid: "42", bindingToken: "current"}}
		reply, err = uc.Login(t.Context(), current, 42, "valid", 0)
		require.NoError(t, err)
		require.Equal(t, codes.Success, reply.Code)
		time.Sleep(time.Minute)
		synctest.Wait()
		require.Equal(t, int64(1), first.renews.Load())
		require.Equal(t, int64(1), current.renews.Load())

		loggedOut, err := uc.Logout(t.Context(), 42)
		require.NoError(t, err)
		require.Equal(t, codes.Success, loggedOut.Code)
		time.Sleep(time.Minute)
		synctest.Wait()
		require.Equal(t, int64(1), current.renews.Load())
	})
}

func TestDrainCancelsAndWaitsForNodeRenewal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		uc, cleanup, err := NewUsecase("ludo-test", new(memoryPlayerRepo), testRoom(1, 4))
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
			recordingSession: recordingSession{uid: "42", bindingToken: "resident"},
			renew: func(ctx context.Context) error {
				deadline, _ := ctx.Deadline()
				budgets <- time.Until(deadline)
				<-ctx.Done()
				canceled <- ctx.Err()
				<-release
				return ctx.Err()
			},
		}
		reply, err := uc.Login(t.Context(), sess, 42, "valid", 0)
		require.NoError(t, err)
		require.Equal(t, codes.Success, reply.Code)
		synctest.Wait()
		time.Sleep(time.Minute)
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
		time.Sleep(time.Minute)
		synctest.Wait()
		require.Equal(t, int64(1), sess.renews.Load())
	})
}

func TestResidentPlayerRenewsNodeWithoutRebinding(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		uc, cleanup, err := NewUsecase("ludo-test", new(memoryPlayerRepo), testRoom(1, 4))
		require.NoError(t, err)
		t.Cleanup(cleanup)
		sess := &residencySession{recordingSession: recordingSession{uid: "42", bindingToken: "resident"}}
		reply, err := uc.Login(t.Context(), sess, 42, "valid", 0)
		require.NoError(t, err)
		require.Equal(t, codes.Success, reply.Code)
		synctest.Wait()

		time.Sleep(time.Minute)
		synctest.Wait()

		require.Equal(t, int64(1), sess.renews.Load())
		binds, _ := sess.counts()
		require.Equal(t, 1, binds)
	})
}

type residencyPusher struct {
	mu   sync.Mutex
	uids []string
}

func (p *residencyPusher) PushToUID(_ context.Context, uid string, _ int32, _ proto.Message) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.uids = append(p.uids, uid)
	return nil
}

func (p *residencyPusher) reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.uids = nil
}

func (p *residencyPusher) sentTo(uid string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Contains(p.uids, uid)
}

type residencySession struct {
	recordingSession
	renews atomic.Int64
	renew  func(context.Context) error
}

func (sess *residencySession) RenewNode(ctx context.Context) error {
	sess.renews.Add(1)
	if sess.renew != nil {
		return sess.renew(ctx)
	}
	return nil
}
