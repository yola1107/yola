package redis_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"yola/locate"

	"github.com/stretchr/testify/require"
)

func TestNodeEpochFencing(t *testing.T) {
	locator, server := newLocator(t)
	ctx := context.Background()
	const serviceName = "game"
	const nodeID = "node-a"
	const epochA = "epoch-a"
	const epochB = "epoch-b"

	require.NoError(t, locator.RegisterNodeEpoch(ctx, serviceName, nodeID, epochA, testTTL))
	err := locator.RegisterNodeEpoch(ctx, serviceName, nodeID, epochB, testTTL)
	require.ErrorIs(t, err, locate.ErrNodeEpochConflict)

	located, err := locator.LocateNodeEpoch(ctx, serviceName, nodeID)
	require.NoError(t, err)
	require.Equal(t, epochA, located)
	require.True(t, strings.HasPrefix(server.Keys()[0], "locate:node:epoch:"))

	require.NoError(t, locator.RenewNodeEpoch(ctx, serviceName, nodeID, epochA, 2*testTTL))
	err = locator.RenewNodeEpoch(ctx, serviceName, nodeID, epochB, testTTL)
	require.ErrorIs(t, err, locate.ErrNodeEpochConflict)

	require.NoError(t, locator.UnregisterNodeEpoch(ctx, serviceName, nodeID, epochA))
	_, err = locator.LocateNodeEpoch(ctx, serviceName, nodeID)
	require.ErrorIs(t, err, locate.ErrNodeEpochNotFound)
	require.NoError(t, locator.UnregisterNodeEpoch(ctx, serviceName, nodeID, epochA))
}

func TestNodeEpochIsScopedByService(t *testing.T) {
	locator, server := newLocator(t)
	ctx := context.Background()
	require.NoError(t, locator.RegisterNodeEpoch(ctx, "ludo", "node-a", "epoch-ludo", testTTL))
	require.NoError(t, locator.RegisterNodeEpoch(ctx, "whot", "node-a", "epoch-whot", testTTL))

	ludoEpoch, err := locator.LocateNodeEpoch(ctx, "ludo", "node-a")
	require.NoError(t, err)
	require.Equal(t, "epoch-ludo", ludoEpoch)
	whotEpoch, err := locator.LocateNodeEpoch(ctx, "whot", "node-a")
	require.NoError(t, err)
	require.Equal(t, "epoch-whot", whotEpoch)
	require.True(t, server.Exists("locate:node:epoch:{bHVkbwBub2RlLWE}"))
	require.True(t, server.Exists("locate:node:epoch:{d2hvdABub2RlLWE}"))
}

func TestNodeEpochExpiry(t *testing.T) {
	locator, server := newLocator(t)
	ctx := context.Background()
	require.NoError(t, locator.RegisterNodeEpoch(ctx, "game", "node-a", "epoch-a", testTTL))
	server.FastForward(testTTL + time.Millisecond)
	_, err := locator.LocateNodeEpoch(ctx, "game", "node-a")
	require.ErrorIs(t, err, locate.ErrNodeEpochNotFound)
	err = locator.RenewNodeEpoch(ctx, "game", "node-a", "epoch-a", testTTL)
	require.ErrorIs(t, err, locate.ErrNodeEpochNotFound)
}

func TestRejectsInvalidNodeEpochInput(t *testing.T) {
	locator, _ := newLocator(t)
	ctx := context.Background()
	require.ErrorIs(t, locator.RegisterNodeEpoch(ctx, "", "node-a", "epoch", testTTL), locate.ErrInvalidNodeEpoch)
	require.ErrorIs(t, locator.RegisterNodeEpoch(ctx, "game", "", "epoch", testTTL), locate.ErrInvalidNodeEpoch)
	require.ErrorIs(t, locator.RegisterNodeEpoch(ctx, "game", "node-a", "", testTTL), locate.ErrInvalidNodeEpoch)
	require.ErrorIs(t, locator.RegisterNodeEpoch(ctx, "game", "node-a", "epoch", time.Nanosecond), locate.ErrInvalidNodeEpoch)
	_, err := locator.LocateNodeEpoch(ctx, "", "node-a")
	require.ErrorIs(t, err, locate.ErrInvalidNodeEpoch)
	require.ErrorIs(t, locator.UnregisterNodeEpoch(ctx, "game", "", "epoch"), locate.ErrInvalidNodeEpoch)
}
