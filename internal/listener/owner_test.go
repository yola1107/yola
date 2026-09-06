package listener

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOwnerBindsLazilyAndReleasesAddress(t *testing.T) {
	owner := New("tcp", "127.0.0.1:0", nil)
	require.Equal(t, "127.0.0.1:0", owner.Addr().String())

	require.NoError(t, owner.Prepare(context.Background()))
	address := owner.Addr().String()
	require.NotEqual(t, "127.0.0.1:0", address)
	require.NoError(t, owner.Close())
	require.NoError(t, owner.Close())

	rebound, err := net.Listen("tcp", address)
	require.NoError(t, err)
	require.NoError(t, rebound.Close())
}

func TestOwnerDoesNotReplaceSuppliedListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	owner := New("ignored", "ignored", listener)

	require.NoError(t, owner.Prepare(context.Background()))
	require.Equal(t, listener.Addr(), owner.Addr())
	require.NoError(t, owner.Close())
}
