package biz

import (
	"testing"

	"yola/test/ludo/internal/biz/player"
	"yola/test/ludo/internal/biz/table"
	"yola/test/ludo/pkg/codes"

	"github.com/stretchr/testify/require"
	grpccodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestLoginSpecifiedTableAndReconnect(t *testing.T) {
	uc, _ := newTestUsecase(t, 2, 4)
	first := &recordingSession{uid: "42", bindingToken: "first"}
	reply, err := uc.Login(t.Context(), first, 42, "valid", 2)
	require.NoError(t, err)
	require.Equal(t, codes.Success, reply.Code)
	require.Equal(t, int32(2), reply.TableID)
	p := uc.pm.GetByID(42)
	chairID := reply.ChairID
	for _, tableID := range []int32{0, 1, 3} {
		sess := &recordingSession{uid: "42", bindingToken: "reconnect"}
		reply, err = uc.Login(t.Context(), sess, 42, "valid", tableID)
		require.NoError(t, err)
		require.Equal(t, codes.Success, reply.Code)
		require.Equal(t, "ReEnter", reply.Msg)
		require.Equal(t, int32(2), reply.TableID)
		require.Equal(t, chairID, reply.ChairID)
		require.Same(t, p, uc.pm.GetByID(42))
		require.Same(t, sess, p.GetSession())
	}
}

func TestLoginSpecifiedTableFailureRollsBack(t *testing.T) {
	uc, _ := newTestUsecase(t, 2, 1)
	first := &recordingSession{uid: "41"}
	firstReply, firstErr := uc.Login(t.Context(), first, 41, "valid", 2)
	require.NoError(t, firstErr)
	require.Equal(t, codes.Success, firstReply.Code)
	for _, tc := range []struct {
		name    string
		tableID int32
		code    int32
		message string
	}{
		{name: "missing", tableID: 3, code: codes.NoTableSpecified, message: "NO_TABLE_SPECIFIED"},
		{name: "full", tableID: 2, code: codes.TableNoSpace, message: "TABLE_NO_SPACE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sess := &recordingSession{uid: "42"}
			reply, err := uc.Login(t.Context(), sess, 42, "valid", tc.tableID)
			require.NoError(t, err)
			require.Equal(t, tc.code, reply.Code)
			require.Equal(t, tc.message, reply.Msg)
			require.Equal(t, player.TableIDPending, reply.TableID)
			require.Nil(t, uc.pm.GetByID(42))
			binds, unbinds := sess.counts()
			require.Equal(t, 1, binds)
			require.Equal(t, 1, unbinds)
		})
	}
	// 失败后同一 UID 可重新选桌，原指定桌玩家不受影响。
	reply, err := uc.Login(t.Context(), &recordingSession{uid: "42"}, 42, "valid", -1)
	require.NoError(t, err)
	require.Equal(t, codes.Success, reply.Code)
	require.Equal(t, int32(1), reply.TableID)
	require.Equal(t, int32(2), uc.pm.GetByID(41).GetTableID())
}

func TestLoginSpecifiedTablePreservesSeatFailure(t *testing.T) {
	uc, _ := newTestUsecase(t, 2, 4)
	sess := &recordingSession{uid: "42", pushErr: status.Error(grpccodes.NotFound, "connection closed")}
	reply, err := uc.Login(t.Context(), sess, 42, "valid", 2)
	require.ErrorIs(t, err, table.ErrPlayerDisconnected)
	require.Nil(t, reply)
	require.Nil(t, uc.pm.GetByID(42))
	binds, unbinds := sess.counts()
	require.Equal(t, 1, binds)
	require.Equal(t, 1, unbinds)
}
