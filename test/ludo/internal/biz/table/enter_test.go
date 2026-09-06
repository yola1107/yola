package table

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"yola/test/internal/mailbox"
	"yola/test/ludo/internal/biz/player"
	"yola/test/ludo/pkg/codes"

	"github.com/stretchr/testify/require"
)

func TestEnterSelectsRequestedTable(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tableID int32
		wantID  int32
		code    int32
		message string
	}{
		{name: "automatic_zero", tableID: 0, wantID: 1},
		{name: "automatic_negative", tableID: -1, wantID: 1},
		{name: "first", tableID: 1, wantID: 1},
		{name: "last", tableID: 2, wantID: 2},
		{name: "missing", tableID: 3, code: codes.NoTableSpecified, message: "NO_TABLE_SPECIFIED"},
		{name: "maximum_id", tableID: 1<<31 - 1, code: codes.NoTableSpecified, message: "NO_TABLE_SPECIFIED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := newStartedManager(t, testRoom(2), new(tableRepoStub))
			p := player.New(&player.Raw{ID: 1, BaseData: &player.BaseData{UID: 1, Money: 500}})
			code, message, err := manager.Enter(t.Context(), p, tc.tableID)
			require.NoError(t, err)
			require.Equal(t, tc.code, code)
			require.Equal(t, tc.message, message)
			require.Equal(t, tc.wantID, p.GetTableID())
		})
	}
}

func TestEnterSpecifiedTableDoesNotOverfillOrFallBack(t *testing.T) {
	room := testRoom(2)
	room.Table.ChairNum = 4
	manager := newStartedManager(t, room, new(tableRepoStub))
	const playerCount = 5
	players := make([]*player.Player, playerCount)
	results := make([]struct {
		code    int32
		message string
		err     error
	}, playerCount)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for index := range playerCount {
		uid := int64(index + 1)
		players[index] = player.New(&player.Raw{ID: uid, BaseData: &player.BaseData{UID: uid, Money: 500}})
		workers.Go(func() {
			<-start
			results[index].code, results[index].message, results[index].err = manager.Enter(t.Context(), players[index], 2)
		})
	}
	close(start)
	workers.Wait()
	chairs := make(map[int32]bool)
	for index, result := range results {
		require.NoError(t, result.err)
		p := players[index]
		if result.code == codes.Success {
			require.Equal(t, int32(2), p.GetTableID())
			require.GreaterOrEqual(t, p.GetChairID(), int32(0))
			require.Less(t, p.GetChairID(), room.Table.ChairNum)
			require.False(t, chairs[p.GetChairID()])
			chairs[p.GetChairID()] = true
			continue
		}
		require.Equal(t, codes.TableNoSpace, result.code)
		require.Equal(t, "TABLE_NO_SPACE", result.message)
		require.Equal(t, player.TableIDPending, p.GetTableID())
	}
	require.Len(t, chairs, int(room.Table.ChairNum))
	require.Zero(t, manager.table(1).seatedCount())
	require.Equal(t, room.Table.ChairNum, manager.table(2).seatedCount())
	code, _, err := manager.Enter(t.Context(), nil, 2)
	require.NoError(t, err)
	require.Equal(t, codes.PlayerInvalid, code)
}

func TestEnterSpecifiedTablePreservesMailboxErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "full", err: mailbox.ErrFull},
		{name: "deadline", err: context.DeadlineExceeded},
		{name: "canceled", err: context.Canceled},
		{name: "stopped", err: mailbox.ErrStopped},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				manager := newStartedManagerWithConfig(t, testRoom(2), new(tableRepoStub), 2, 1, 1)
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				if tc.name == "stopped" {
					require.NoError(t, manager.Close(ctx))
				} else {
					release := make(chan struct{})
					defer close(release)
					executor := manager.mailboxes.Executor(1)
					require.NoError(t, executor.TryPost(func() { <-release }))
					synctest.Wait()
					if tc.name == "full" {
						require.NoError(t, executor.TryPost(func() {}))
					}
					if tc.name == "canceled" {
						cancel()
					}
				}
				p := player.New(&player.Raw{ID: 1, BaseData: &player.BaseData{UID: 1, Money: 500}})
				_, _, err := manager.Enter(ctx, p, 2)
				require.ErrorIs(t, err, tc.err)
				// Close 在解除阻塞后排空队列，保证超时任务不会晚到入座。
				t.Cleanup(func() {
					require.NoError(t, manager.Close(context.Background()))
					require.Equal(t, player.TableIDPending, p.GetTableID())
					for _, gameTable := range manager.tables {
						require.Zerof(t, gameTable.seatedCount(), "table %d", gameTable.ID)
					}
				})
			})
		})
	}
}
