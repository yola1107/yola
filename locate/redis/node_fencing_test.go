package redis_test

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"yola/locate"

	"github.com/stretchr/testify/require"
)

func TestNodeBindingRejectsOldEpochAndPreservesRestart(t *testing.T) {
	locator, _ := newLocator(t)
	assertNodeBindingEpochHandoff(t, locator, "game")
}

func TestNodeBindingRequiresLiveEpoch(t *testing.T) {
	for _, expired := range []bool{false, true} {
		name := "missing"
		if expired {
			name = "expired"
		}
		t.Run(name, func(t *testing.T) {
			locator, server := newLocator(t)
			ctx := t.Context()
			if expired {
				require.NoError(t, locator.RegisterNodeEpoch(ctx, "game", "node-a", "epoch-a", testTTL))
				require.NoError(t, locator.BindNode(ctx, "game", "player", "node-a", "epoch-a"))
				server.FastForward(testTTL + time.Millisecond)
			}
			require.ErrorIs(t, locator.BindNode(ctx, "game", "player", "node-a", "epoch-a"), locate.ErrNodeEpochNotFound)
			require.ErrorIs(t, locator.UnbindNode(ctx, "game", "player", "node-a", "epoch-a"), locate.ErrNodeEpochNotFound)
			nodeID, err := locator.LocateNode(ctx, "game", "player")
			if !expired {
				require.ErrorIs(t, err, locate.ErrNodeNotFound)
				return
			}
			require.NoError(t, err)
			require.Equal(t, "node-a", nodeID)
			require.Equal(t, testNodeTTL-testTTL-time.Millisecond, server.TTL("locate:node:{Z2FtZQ}:cGxheWVy"))
		})
	}
}

func TestNodeKeysShareServiceHashTag(t *testing.T) {
	locator, server := newLocator(t)
	ctx := t.Context()
	const service = "game-a"
	for _, nodeID := range []string{"node-a", "node:{}\x00/节点"} {
		require.NoError(t, locator.RegisterNodeEpoch(ctx, service, nodeID, "epoch", testTTL))
	}
	for _, uid := range []string{"player-a", "player:{}\x00/玩家", "node-a"} {
		require.NoError(t, locator.BindNode(ctx, service, uid, "node-a", "epoch"))
	}
	require.Len(t, server.Keys(), 5)
	for _, key := range server.Keys() {
		_, suffix, found := strings.Cut(key, "{")
		require.True(t, found)
		tag, _, found := strings.Cut(suffix, "}")
		require.True(t, found)
		require.Equal(t, "Z2FtZS1h", tag)
	}
	require.NoError(t, locator.RegisterNodeEpoch(ctx, "game-b", "node-a", "epoch", testTTL))
	require.NoError(t, locator.BindNode(ctx, "game-b", "player-a", "node-a", "epoch"))
	require.Len(t, server.Keys(), 7)
	require.True(t, server.Exists("locate:node:{Z2FtZS1i}:cGxheWVyLWE"))
	require.True(t, server.Exists("locate:node:epoch:{Z2FtZS1i}:bm9kZS1h"))
}

// assertNodeBindingEpochHandoff 在替身和真实 Redis 上验证相同的修改权与重启契约。
func assertNodeBindingEpochHandoff(t *testing.T, locator locate.Locator, service string) {
	t.Helper()
	ctx := t.Context()
	const uid = "player"
	require.NoError(t, locator.RegisterNodeEpoch(ctx, service, "node-a", "old", testTTL))
	require.NoError(t, locator.RegisterNodeEpoch(ctx, service, "node-b", "other", testTTL))
	require.NoError(t, locator.BindNode(ctx, service, uid, "node-a", "old"))
	require.NoError(t, locator.BindNode(ctx, service, uid, "node-b", "other"))
	require.NoError(t, locator.UnbindNode(ctx, service, uid, "node-a", "old"))
	nodeID, err := locator.LocateNode(ctx, service, uid)
	require.NoError(t, err)
	require.Equal(t, "node-b", nodeID)

	require.NoError(t, locator.UnregisterNodeEpoch(ctx, service, "node-a", "old"))
	require.NoError(t, locator.RegisterNodeEpoch(ctx, service, "node-a", "new", testTTL))
	require.ErrorIs(t, locator.BindNode(ctx, service, uid, "node-a", "old"), locate.ErrNodeEpochConflict)
	require.ErrorIs(t, locator.UnbindNode(ctx, service, uid, "node-a", "old"), locate.ErrNodeEpochConflict)
	nodeID, err = locator.LocateNode(ctx, service, uid)
	require.NoError(t, err)
	require.Equal(t, "node-b", nodeID)

	require.NoError(t, locator.BindNode(ctx, service, uid, "node-a", "new"))
	require.ErrorIs(t, locator.UnbindNode(ctx, service, uid, "node-a", "old"), locate.ErrNodeEpochConflict)
	nodeID, err = locator.LocateNode(ctx, service, uid)
	require.NoError(t, err)
	require.Equal(t, "node-a", nodeID)

	require.NoError(t, locator.UnregisterNodeEpoch(ctx, service, "node-a", "new"))
	nodeID, err = locator.LocateNode(ctx, service, uid)
	require.NoError(t, err)
	require.Equal(t, "node-a", nodeID)
	require.ErrorIs(t, locator.BindNode(ctx, service, uid, "node-a", "new"), locate.ErrNodeEpochNotFound)
	require.ErrorIs(t, locator.UnbindNode(ctx, service, uid, "node-a", "new"), locate.ErrNodeEpochNotFound)
	require.NoError(t, locator.RegisterNodeEpoch(ctx, service, "node-a", "restarted", testTTL))
	nodeID, err = locator.LocateNode(ctx, service, uid)
	require.NoError(t, err)
	require.Equal(t, "node-a", nodeID)
	require.NoError(t, locator.UnbindNode(ctx, service, uid, "node-a", "restarted"))
	require.NoError(t, locator.UnbindNode(ctx, service, uid, "node-a", "restarted"))
	require.ErrorIs(t, locator.UnbindNode(ctx, service, uid, "node-a", "old"), locate.ErrNodeEpochConflict)
	_, err = locator.LocateNode(ctx, service, uid)
	require.ErrorIs(t, err, locate.ErrNodeNotFound)

	// 相同 NodeID/UID 在其他 service 中不能借用本 service 的 epoch。
	require.ErrorIs(t, locator.BindNode(ctx, service+"-other", uid, "node-a", "restarted"), locate.ErrNodeEpochNotFound)
}

func nodeBindingTestKeys(service string) []string {
	tag := "{" + base64.RawURLEncoding.EncodeToString([]byte(service)) + "}:"
	return []string{
		"locate:node:" + tag + base64.RawURLEncoding.EncodeToString([]byte("player")),
		"locate:node:epoch:" + tag + base64.RawURLEncoding.EncodeToString([]byte("node-a")),
		"locate:node:epoch:" + tag + base64.RawURLEncoding.EncodeToString([]byte("node-b")),
	}
}
