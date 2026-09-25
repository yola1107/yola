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
