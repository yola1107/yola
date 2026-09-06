package biz

import (
	"context"
	"strconv"
	"testing"
	"testing/synctest"
	"time"

	"yola/test/ludo/api/v1"
	"yola/test/ludo/internal/biz/table"
	"yola/test/ludo/pkg/codes"

	"github.com/stretchr/testify/require"
)

type cleanupContextSession struct {
	recordingSession
	cleanupErr    error
	cleanupBudget time.Duration
}

func (sess *cleanupContextSession) UnbindNode(ctx context.Context) error {
	sess.cleanupErr = ctx.Err()
	if deadline, ok := ctx.Deadline(); ok {
		sess.cleanupBudget = time.Until(deadline)
	}
	return sess.recordingSession.UnbindNode(ctx)
}

func TestLoginWaitsForEntryAndKeepsCleanupBudget(t *testing.T) {
	for _, tc := range []struct {
		name      string
		reconnect bool
		timeout   bool
	}{
		{name: "enter_after_three_seconds"},
		{name: "reconnect_after_three_seconds", reconnect: true},
		{name: "enter_timeout", timeout: true},
		{name: "reconnect_timeout", reconnect: true, timeout: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				uc, _ := newTestUsecase(t, 1, 4)
				originalSession := &recordingSession{uid: "41"}
				login(t, uc, originalSession, 41)
				seated := uc.pm.GetByID(41)
				release := make(chan struct{})
				blockDone := make(chan error, 1)
				go func() {
					blockDone <- uc.tm.CallPlayer(t.Context(), seated, func(*table.Table) error {
						<-release
						return nil
					})
				}()
				synctest.Wait()
				uid := int64(42)
				if tc.reconnect {
					uid = 41
				}
				sess := &cleanupContextSession{recordingSession: recordingSession{uid: strconv.FormatInt(uid, 10)}}
				var reply *v1.LoginRsp
				var loginErr error
				var loginElapsed time.Duration
				loginDone := make(chan struct{})
				go func() {
					started := time.Now()
					reply, loginErr = uc.Login(t.Context(), sess, uid, "valid", 1)
					loginElapsed = time.Since(started)
					close(loginDone)
				}()
				synctest.Wait()
				if tc.timeout {
					<-loginDone
				} else {
					time.Sleep(3 * time.Second)
				}
				close(release)
				require.NoError(t, <-blockDone)
				<-loginDone
				require.NoError(t, uc.tm.Close(context.Background()))
				if !tc.timeout {
					require.NoError(t, loginErr)
					require.Equal(t, codes.Success, reply.Code)
					require.Same(t, sess, uc.pm.GetByID(uid).GetSession())
					return
				}
				require.ErrorIs(t, loginErr, context.DeadlineExceeded)
				require.Equal(t, 5*time.Second, loginElapsed)
				require.Nil(t, reply)
				require.NoError(t, sess.cleanupErr)
				require.Equal(t, 2*time.Second, sess.cleanupBudget)
				_, unbinds := sess.counts()
				require.Equal(t, 1, unbinds)
				require.Empty(t, sess.pushes)
				if tc.reconnect {
					require.Same(t, originalSession, seated.GetSession())
					return
				}
				require.Nil(t, uc.pm.GetByID(uid))
			})
		})
	}
}
