package redis_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"yola/locate"

	"github.com/stretchr/testify/require"
)

func TestGateBindingStorage(t *testing.T) {
	locator, server := newLocator(t)
	binding := newBinding("gate-a", "conn-a")
	_, _, err := locator.BindGate(context.Background(), binding, testTTL)
	require.NoError(t, err)
	require.Len(t, server.Keys(), 1)
	require.True(t, strings.HasPrefix(server.Keys()[0], "locate:gate:"))

	stored, err := server.Get(server.Keys()[0])
	require.NoError(t, err)
	want, err := json.Marshal(binding)
	require.NoError(t, err)
	require.Equal(t, string(want), stored)
	require.Contains(t, stored, `"uid":"synthetic-player"`)
	require.NotContains(t, stored, `"key"`)
}

func TestLocateKeyUsesCanonicalEncoding(t *testing.T) {
	locator, server := newLocator(t)
	binding := newBinding("gate-a", "conn-a")
	_, _, err := locator.BindGate(context.Background(), binding, testTTL)
	require.NoError(t, err)
	require.NoError(t, locator.BindNode(context.Background(), binding.ServiceName, binding.UID, "node-a"))
	require.ElementsMatch(t, []string{
		"locate:gate:{Z2FtZQBzeW50aGV0aWMtcGxheWVy}",
		"locate:node:{Z2FtZQBzeW50aGV0aWMtcGxheWVy}",
	}, server.Keys())
}

func TestRejectsInvalidInput(t *testing.T) {
	locator, _ := newLocator(t)
	ctx := context.Background()
	valid := newBinding("gate-a", "conn-a")

	_, _, err := locator.BindGate(ctx, locate.GateBinding{}, testTTL)
	require.ErrorIs(t, err, locate.ErrInvalidGateBinding)
	_, _, err = locator.BindGate(ctx, valid, time.Nanosecond)
	require.ErrorIs(t, err, locate.ErrInvalidGateTTL)
	_, err = locator.LocateGate(ctx, "", valid.UID)
	require.ErrorIs(t, err, locate.ErrInvalidGateBinding)
	_, err = locator.RenewGateLease(ctx, locate.GateBinding{}, testTTL)
	require.ErrorIs(t, err, locate.ErrInvalidGateBinding)
	require.ErrorIs(t, locator.UnbindGate(ctx, locate.GateBinding{}), locate.ErrInvalidGateBinding)
	require.ErrorIs(t, locator.BindNode(ctx, "", "", ""), locate.ErrInvalidNodeBinding)
	_, err = locator.LocateNode(ctx, "", valid.UID)
	require.ErrorIs(t, err, locate.ErrInvalidNodeBinding)
	require.ErrorIs(t, locator.UnbindNode(ctx, "", "", ""), locate.ErrInvalidNodeBinding)
}
