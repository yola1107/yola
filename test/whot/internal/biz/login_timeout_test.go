package biz

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"testing/synctest"
	"time"

	"yola/test/whot/internal/biz/table"
	"yola/test/whot/pkg/codes"

	"github.com/stretchr/testify/require"
)

func TestLoginTimeoutKeepsCleanupBudget(t *testing.T) {
	for _, reconnect := range []bool{false, true} {
		name := "enter"
		if reconnect {
			name = "reconnect"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				uc, cleanup, err := NewUsecase(new(usecaseRepoStub), whotDrainTestRoom())
				require.NoError(t, err)
				t.Cleanup(cleanup)
				original := &connectionSession{uid: "41", bindingToken: "original"}
				reply, err := uc.Login(t.Context(), original, 41, "valid")
				require.NoError(t, err)
				require.Equal(t, codes.Success, reply.Code)
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
				if reconnect {
					uid = 41
				}
				unbindErr := errors.New("unbind failed")
				sess := &cleanupContextSession{connectionSession: connectionSession{
					uid: strconv.FormatInt(uid, 10), bindingToken: "candidate", unbindErr: unbindErr,
				}}
				started := time.Now()
				reply, err = uc.Login(t.Context(), sess, uid, "valid")
				elapsed := time.Since(started)
				close(release)
				require.NoError(t, <-blockDone)
				require.NoError(t, uc.tm.Close(context.Background()))

				require.ErrorIs(t, err, context.DeadlineExceeded)
				require.ErrorIs(t, err, unbindErr)
				require.Nil(t, reply)
				require.Equal(t, 2*time.Second, elapsed)
				require.NoError(t, sess.cleanupErr)
				require.Equal(t, 2*time.Second, sess.cleanupBudget)
				require.Equal(t, 1, sess.unbindCount())
				if reconnect {
					require.Same(t, original, seated.GetSession())
				} else {
					require.Nil(t, uc.pm.GetByID(uid))
				}
			})
		})
	}
}

type cleanupContextSession struct {
	connectionSession
	cleanupErr    error
	cleanupBudget time.Duration
}

func (sess *cleanupContextSession) UnbindNode(ctx context.Context) error {
	sess.cleanupErr = ctx.Err()
	if deadline, ok := ctx.Deadline(); ok {
		sess.cleanupBudget = time.Until(deadline)
	}
	return errors.Join(sess.connectionSession.UnbindNode(ctx), ctx.Err())
}
