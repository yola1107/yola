package redis_test

import (
	"context"
	"testing"
	"time"

	"yola/locate"

	"github.com/stretchr/testify/require"
)

const (
	testNodeService = "whot"
	testNodeUID     = "synthetic-player"
	testNodeTTL     = 6 * time.Hour
)

func TestNodeRenewalPreservesBindingOwnership(t *testing.T) {
	locator, server := newLocator(t)
	ctx := t.Context()
	const key = "locate:node:{d2hvdA}:c3ludGhldGljLXBsYXllcg"
	for _, nodeID := range []string{"node-a", "node-b"} {
		require.NoError(t, locator.RegisterNodeEpoch(ctx, testNodeService, nodeID, nodeID, 2*testNodeTTL))
	}
	require.ErrorIs(t, locator.RenewNode(ctx, testNodeService, testNodeUID, "node-a", "node-a"), locate.ErrNodeNotFound)
	require.NoError(t, locator.BindNode(ctx, testNodeService, testNodeUID, "node-a", "node-a"))
	server.FastForward(time.Hour)
	require.NoError(t, locator.RenewNode(ctx, testNodeService, testNodeUID, "node-a", "node-a"))
	require.Equal(t, testNodeTTL, server.TTL(key))
	require.NoError(t, locator.BindNode(ctx, testNodeService, testNodeUID, "node-b", "node-b"))
	server.FastForward(time.Hour)
	ttl := server.TTL(key)
	require.ErrorIs(t, locator.RenewNode(ctx, testNodeService, testNodeUID, "node-a", "node-a"), locate.ErrNodeConflict)
	require.ErrorIs(t, locator.RenewNode(ctx, testNodeService, testNodeUID, "node-b", "stale"), locate.ErrNodeEpochConflict)
	require.Equal(t, ttl, server.TTL(key), "rejected renewal must not extend the replacement binding")
	nodeID, err := locator.LocateNode(ctx, testNodeService, testNodeUID)
	require.NoError(t, err)
	require.Equal(t, "node-b", nodeID)

	// 原 NodeID 重启后用新 epoch 继承绑定，无需再次覆盖写入。
	require.NoError(t, locator.UnregisterNodeEpoch(ctx, testNodeService, "node-b", "node-b"))
	require.NoError(t, locator.RegisterNodeEpoch(ctx, testNodeService, "node-b", "restarted", testTTL))
	require.NoError(t, locator.RenewNode(ctx, testNodeService, testNodeUID, "node-b", "restarted"))
	require.Equal(t, testNodeTTL, server.TTL(key))
	// 显式 Bind 仍可接管；普通保活只允许使用 RenewNode。
	require.NoError(t, locator.BindNode(ctx, testNodeService, testNodeUID, "node-a", "node-a"))
	require.NoError(t, locator.UnbindNode(ctx, testNodeService, testNodeUID, "node-a", "node-a"))
	require.ErrorIs(t, locator.RenewNode(ctx, testNodeService, testNodeUID, "node-a", "node-a"), locate.ErrNodeNotFound)
	require.False(t, server.Exists(key))
}

func TestNodeRenewalRejectsInvalidArguments(t *testing.T) {
	locator, server := newLocator(t)
	for _, input := range []struct {
		service, uid, node, epoch string
		want                      error
	}{
		{service: "bad/service", uid: "uid", node: "node", epoch: "epoch", want: locate.ErrInvalidNodeBinding},
		{service: "game", node: "node", epoch: "epoch", want: locate.ErrInvalidNodeBinding},
		{service: "game", uid: "uid", epoch: "epoch", want: locate.ErrInvalidNodeBinding},
		{service: "game", uid: "uid", node: "node", want: locate.ErrInvalidNodeEpoch},
	} {
		require.ErrorIs(t, locator.RenewNode(t.Context(), input.service, input.uid, input.node, input.epoch), input.want)
	}
	require.Empty(t, server.Keys())
}

func TestNodeBindingFollowsBusinessLifecycle(t *testing.T) {
	locator, server := newLocator(t)
	ctx := context.Background()
	// 本测试只推进 binding TTL，进程租约覆盖整个业务时间窗口。
	require.NoError(t, locator.RegisterNodeEpoch(ctx, testNodeService, "node-a", "epoch-a", 2*testNodeTTL))
	require.NoError(t, locator.RegisterNodeEpoch(ctx, testNodeService, "node-b", "epoch-b", 2*testNodeTTL))
	require.NoError(t, locator.BindNode(ctx, testNodeService, testNodeUID, "node-a", "epoch-a"))
	bindingKey := "locate:node:{d2hvdA}:c3ludGhldGljLXBsYXllcg"
	ttl := server.TTL(bindingKey)
	require.Equal(t, testNodeTTL, ttl)

	located, err := locator.LocateNode(ctx, testNodeService, testNodeUID)
	require.NoError(t, err)
	require.Equal(t, "node-a", located)

	server.FastForward(ttl / 2)
	require.NoError(t, locator.BindNode(ctx, testNodeService, testNodeUID, "node-b", "epoch-b"))
	require.NoError(t, locator.UnbindNode(ctx, testNodeService, testNodeUID, "node-a", "epoch-a"))
	require.Equal(t, testNodeTTL, server.TTL(bindingKey))
	located, err = locator.LocateNode(ctx, testNodeService, testNodeUID)
	require.NoError(t, err)
	require.Equal(t, "node-b", located)

	server.FastForward(ttl - time.Millisecond)
	located, err = locator.LocateNode(ctx, testNodeService, testNodeUID)
	require.NoError(t, err)
	require.Equal(t, "node-b", located)
	server.FastForward(2 * time.Millisecond)
	_, err = locator.LocateNode(ctx, testNodeService, testNodeUID)
	require.ErrorIs(t, err, locate.ErrNodeNotFound)

	require.NoError(t, locator.BindNode(ctx, testNodeService, testNodeUID, "node-b", "epoch-b"))
	require.NoError(t, locator.UnbindNode(ctx, testNodeService, testNodeUID, "node-b", "epoch-b"))
	require.NoError(t, locator.UnbindNode(ctx, testNodeService, testNodeUID, "node-b", "epoch-b"))
	_, err = locator.LocateNode(ctx, testNodeService, testNodeUID)
	require.ErrorIs(t, err, locate.ErrNodeNotFound)
}

func TestNodeBindingIsScopedByService(t *testing.T) {
	locator, _ := newLocator(t)
	ctx := context.Background()
	require.NoError(t, locator.RegisterNodeEpoch(ctx, "whot", "whot-node", "epoch-whot", testTTL))
	require.NoError(t, locator.RegisterNodeEpoch(ctx, "ludo", "ludo-node", "epoch-ludo", testTTL))
	require.NoError(t, locator.BindNode(ctx, "whot", testNodeUID, "whot-node", "epoch-whot"))
	require.NoError(t, locator.BindNode(ctx, "ludo", testNodeUID, "ludo-node", "epoch-ludo"))

	located, err := locator.LocateNode(ctx, "whot", testNodeUID)
	require.NoError(t, err)
	require.Equal(t, "whot-node", located)
	located, err = locator.LocateNode(ctx, "ludo", testNodeUID)
	require.NoError(t, err)
	require.Equal(t, "ludo-node", located)
}
