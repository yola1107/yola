package redis_test

import (
	"context"
	"testing"

	locateredis "yola/locate/redis"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestNewNilClientReturnsNilLocator(t *testing.T) {
	require.Nil(t, locateredis.New(nil))
	var client *redis.Client
	require.Nil(t, locateredis.New(client))
}

func TestPing(t *testing.T) {
	locator, _ := newLocator(t)
	require.NoError(t, locator.Ping(context.Background()))
}
