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
)

func TestNodeBindingFollowsBusinessLifecycle(t *testing.T) {
	locator, server := newLocator(t)
	ctx := context.Background()
	require.NoError(t, locator.BindNode(ctx, testNodeService, testNodeUID, "node-a"))
	ttl := server.TTL(server.Keys()[0])
	require.Equal(t, 6*time.Hour, ttl)

	located, err := locator.LocateNode(ctx, testNodeService, testNodeUID)
	require.NoError(t, err)
	require.Equal(t, "node-a", located)

	server.FastForward(ttl / 2)
	require.NoError(t, locator.BindNode(ctx, testNodeService, testNodeUID, "node-b"))
	require.NoError(t, locator.UnbindNode(ctx, testNodeService, testNodeUID, "node-a"))
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

	require.NoError(t, locator.BindNode(ctx, testNodeService, testNodeUID, "node-b"))
	require.NoError(t, locator.UnbindNode(ctx, testNodeService, testNodeUID, "node-b"))
	require.NoError(t, locator.UnbindNode(ctx, testNodeService, testNodeUID, "node-b"))
	_, err = locator.LocateNode(ctx, testNodeService, testNodeUID)
	require.ErrorIs(t, err, locate.ErrNodeNotFound)
}

func TestNodeBindingIsScopedByService(t *testing.T) {
	locator, _ := newLocator(t)
	ctx := context.Background()
	require.NoError(t, locator.BindNode(ctx, "whot", testNodeUID, "whot-node"))
	require.NoError(t, locator.BindNode(ctx, "ludo", testNodeUID, "ludo-node"))

	located, err := locator.LocateNode(ctx, "whot", testNodeUID)
	require.NoError(t, err)
	require.Equal(t, "whot-node", located)
	located, err = locator.LocateNode(ctx, "ludo", testNodeUID)
	require.NoError(t, err)
	require.Equal(t, "ludo-node", located)
}
