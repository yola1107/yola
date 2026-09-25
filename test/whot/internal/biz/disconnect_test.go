package biz

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"

	"yola/test/whot/internal/biz/player"
	"yola/test/whot/internal/biz/table"
	"yola/test/whot/pkg/codes"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestDisconnectOverlappingReconnectKeepsNewSessionOnline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		uc, cleanup, err := NewUsecase(new(usecaseRepoStub), whotDrainTestRoom())
		require.NoError(t, err)
		t.Cleanup(cleanup)
		original := &connectionSession{uid: "42", bindingToken: "original"}
		reply, err := uc.Login(t.Context(), original, 42, "valid")
		require.NoError(t, err)
		require.Equal(t, codes.Success, reply.Code)
		p := uc.pm.GetByID(42)

		tokenRead := make(chan struct{})
		release := make(chan struct{})
		releaseToken := sync.OnceFunc(func() { close(release) })
		t.Cleanup(releaseToken)
		disconnected := &pausedTokenSession{Session: original, tokenRead: tokenRead, release: release}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		disconnectDone := make(chan error, 1)
		go func() { disconnectDone <- uc.Disconnect(ctx, disconnected) }()
		<-tokenRead

		reconnected := &connectionSession{uid: "42", bindingToken: "reconnected"}
		reply, err = uc.Login(t.Context(), reconnected, 42, "valid")
		require.NoError(t, err)
		require.Equal(t, codes.Success, reply.Code)
		// 旧事件恢复后无法进入桌任务，不能依赖后续回调纠正离线状态。
		cancel()
		releaseToken()
		disconnectErr := <-disconnectDone
		require.Same(t, reconnected, p.GetSession())
		require.False(t, p.IsOffline(), "旧连接通知覆盖了重连状态")
		require.NoError(t, disconnectErr)
	})
}

func TestStaleDisconnectDoesNotClearCurrentSessionOfflineState(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		uc, cleanup, err := NewUsecase(new(usecaseRepoStub), whotDrainTestRoom())
		require.NoError(t, err)
		t.Cleanup(cleanup)
		original := &connectionSession{uid: "42", bindingToken: "original"}
		reply, err := uc.Login(t.Context(), original, 42, "valid")
		require.NoError(t, err)
		require.Equal(t, codes.Success, reply.Code)
		p := uc.pm.GetByID(42)

		blocked := make(chan struct{})
		release := make(chan struct{})
		releaseTable := sync.OnceFunc(func() { close(release) })
		t.Cleanup(releaseTable)
		blockDone := make(chan error, 1)
		go func() {
			blockDone <- uc.tm.CallPlayer(t.Context(), p, func(*table.Table) error {
				close(blocked)
				<-release
				return nil
			})
		}()
		<-blocked

		pushStarted := make(chan struct{})
		releasePush := make(chan struct{})
		resumePush := sync.OnceFunc(func() { close(releasePush) })
		t.Cleanup(resumePush)
		reconnected := &pausedPushSession{
			connectionSession: connectionSession{uid: "42", bindingToken: "reconnected"},
			pause:             sync.OnceFunc(func() { close(pushStarted); <-releasePush }),
		}
		loginDone := make(chan error, 1)
		go func() {
			_, loginErr := uc.Login(t.Context(), reconnected, 42, "valid")
			loginDone <- loginErr
		}()
		synctest.Wait()
		disconnectDone := make(chan error, 1)
		go func() { disconnectDone <- uc.Disconnect(t.Context(), original) }()
		synctest.Wait()

		// 桌任务按重连、旧断线的顺序执行；新连接在重连推送期间断开。
		releaseTable()
		<-pushStarted
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		require.ErrorIs(t, uc.Disconnect(ctx, reconnected), context.Canceled)
		resumePush()
		require.NoError(t, <-blockDone)
		require.NoError(t, <-loginDone)
		require.NoError(t, <-disconnectDone)
		require.Same(t, reconnected, p.GetSession())
		require.True(t, p.IsOffline(), "旧连接回调清除了新连接的断线状态")
	})
}

type pausedTokenSession struct {
	player.Session
	tokenRead chan struct{}
	release   chan struct{}
}

func (sess *pausedTokenSession) BindingToken() string {
	close(sess.tokenRead)
	<-sess.release
	return sess.Session.BindingToken()
}

type pausedPushSession struct {
	connectionSession
	pause func()
}

func (sess *pausedPushSession) Push(ctx context.Context, command int32, msg proto.Message) error {
	sess.pause()
	return sess.connectionSession.Push(ctx, command, msg)
}
