package redis_test

import (
	"testing"
	"time"

	"yola/locate"
	locateredis "yola/locate/redis"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

const testTTL = time.Minute

func newLocator(t *testing.T) (locate.Locator, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	return locateredis.New(client), server
}

func newBinding(gateID, connID string) locate.GateBinding {
	return locate.GateBinding{
		ServiceName:  "game",
		UID:          "synthetic-player",
		GateID:       gateID,
		GateEndpoint: "grpc://" + gateID + ":9000",
		ConnID:       connID,
		BindingToken: "binding-" + connID,
	}
}
