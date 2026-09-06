package redis_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"yola/locate"

	"github.com/stretchr/testify/require"
)

func TestBindGateFencesPreviousBinding(t *testing.T) {
	locator, _ := newLocator(t)
	ctx := context.Background()

	oldLease, previous, err := locator.BindGate(ctx, newBinding("gate-a", "conn-a"), testTTL)
	require.NoError(t, err)
	require.Nil(t, previous)

	current, previous, err := locator.BindGate(ctx, newBinding("gate-b", "conn-b"), testTTL)
	require.NoError(t, err)
	require.Equal(t, oldLease.Binding, *previous)
	require.NotEqual(t, oldLease.Binding.BindingToken, current.Binding.BindingToken)

	_, err = locator.RenewGateLease(ctx, oldLease.Binding, testTTL)
	require.ErrorIs(t, err, locate.ErrGateConflict)
	require.NoError(t, locator.UnbindGate(ctx, oldLease.Binding))

	located, err := locator.LocateGate(ctx, current.Binding.ServiceName, current.Binding.UID)
	require.NoError(t, err)
	require.Equal(t, current, located)

	renewed, err := locator.RenewGateLease(ctx, current.Binding, 2*testTTL)
	require.NoError(t, err)
	require.Equal(t, current.Binding, renewed.Binding)
	require.Equal(t, 2*testTTL, renewed.TTL)

	require.NoError(t, locator.UnbindGate(ctx, current.Binding))
	require.NoError(t, locator.UnbindGate(ctx, current.Binding))
	_, err = locator.LocateGate(ctx, current.Binding.ServiceName, current.Binding.UID)
	require.ErrorIs(t, err, locate.ErrGateNotFound)
}

func TestConcurrentBindGateLeavesOneCurrentBinding(t *testing.T) {
	locator, _ := newLocator(t)
	candidates := []locate.GateBinding{
		newBinding("gate-a", "conn-a"),
		newBinding("gate-b", "conn-b"),
	}
	start := make(chan struct{})
	type result struct {
		lease    locate.GateLease
		previous *locate.GateBinding
		err      error
	}
	results := make(chan result, len(candidates))
	var wg sync.WaitGroup
	for _, candidate := range candidates {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			lease, previous, err := locator.BindGate(context.Background(), candidate, testTTL)
			results <- result{lease: lease, previous: previous, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	bound := make([]locate.GateBinding, 0, len(candidates))
	previousCount := 0
	for result := range results {
		require.NoError(t, result.err)
		bound = append(bound, result.lease.Binding)
		if result.previous != nil {
			previousCount++
		}
	}
	require.Equal(t, 1, previousCount)

	current, err := locator.LocateGate(context.Background(), "game", "synthetic-player")
	require.NoError(t, err)
	require.Contains(t, bound, current.Binding)
	for _, binding := range bound {
		_, err = locator.RenewGateLease(context.Background(), binding, testTTL)
		if binding == current.Binding {
			require.NoError(t, err)
		} else {
			require.ErrorIs(t, err, locate.ErrGateConflict)
		}
	}
}

func TestBindGateDoesNotOverwriteInvalidCurrentBinding(t *testing.T) {
	locator, server := newLocator(t)
	_, _, err := locator.BindGate(context.Background(), newBinding("gate-a", "conn-a"), testTTL)
	require.NoError(t, err)
	require.Len(t, server.Keys(), 1)

	key := server.Keys()[0]
	const invalid = `{"service_name":"game","uid":"synthetic-player"}`
	require.NoError(t, server.Set(key, invalid))
	server.SetTTL(key, testTTL)

	_, _, err = locator.BindGate(context.Background(), newBinding("gate-b", "conn-b"), testTTL)
	require.ErrorIs(t, err, locate.ErrInvalidGateBinding)
	stored, err := server.Get(key)
	require.NoError(t, err)
	require.Equal(t, invalid, stored)
}

func TestLeaseExpiryAndServiceScope(t *testing.T) {
	locator, server := newLocator(t)
	ctx := context.Background()
	game := newBinding("gate-game", "conn-game")
	room := newBinding("gate-room", "conn-room")
	room.ServiceName = "room"

	_, _, err := locator.BindGate(ctx, game, testTTL)
	require.NoError(t, err)
	_, _, err = locator.BindGate(ctx, room, testTTL)
	require.NoError(t, err)
	require.Len(t, server.Keys(), 2)

	gameLease, err := locator.LocateGate(ctx, game.ServiceName, game.UID)
	require.NoError(t, err)
	require.Equal(t, game, gameLease.Binding)
	roomLease, err := locator.LocateGate(ctx, room.ServiceName, room.UID)
	require.NoError(t, err)
	require.Equal(t, room, roomLease.Binding)

	server.FastForward(testTTL + time.Millisecond)
	_, err = locator.LocateGate(ctx, game.ServiceName, game.UID)
	require.ErrorIs(t, err, locate.ErrGateNotFound)
	_, err = locator.LocateGate(ctx, room.ServiceName, room.UID)
	require.ErrorIs(t, err, locate.ErrGateNotFound)
}
