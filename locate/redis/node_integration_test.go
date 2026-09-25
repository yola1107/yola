package redis_test

import (
	"context"
	"crypto/rand"
	"os"
	"strings"
	"testing"
	"time"

	locateredis "yola/locate/redis"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestNodeBindingRedisIntegration(t *testing.T) {
	for _, mode := range []string{"standalone", "cluster"} {
		t.Run(mode, func(t *testing.T) {
			variable := "YOLA_REDIS_INTEGRATION"
			if mode == "cluster" {
				variable = "YOLA_REDIS_CLUSTER_INTEGRATION"
			}
			address := os.Getenv(variable)
			if address == "" {
				t.Skipf("set %s to an explicitly provisioned disposable Redis instance", variable)
			}
			require.NotEqual(t, "1", address, "the integration test requires an explicit isolated address")
			var client redis.UniversalClient
			if mode == "cluster" {
				client = redis.NewClusterClient(&redis.ClusterOptions{
					Addrs: strings.Split(address, ","), Password: os.Getenv("YOLA_REDIS_PASSWORD"),
				})
			} else {
				client = redis.NewClient(&redis.Options{Addr: address, Password: os.Getenv("YOLA_REDIS_PASSWORD")})
			}
			t.Cleanup(func() { require.NoError(t, client.Close()) })
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			pingErr := client.Ping(ctx).Err()
			cancel()
			require.NoError(t, pingErr)
			service := "yola-i48-" + rand.Text()
			keys := nodeBindingTestKeys(service)
			t.Cleanup(func() {
				cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancelCleanup()
				require.NoError(t, client.Del(cleanupCtx, keys...).Err())
			})
			if mode == "cluster" {
				var slot int64
				for index, key := range keys {
					current, err := client.ClusterKeySlot(t.Context(), key).Result()
					require.NoError(t, err)
					if index == 0 {
						slot = current
					} else {
						require.Equal(t, slot, current)
					}
				}
			}
			assertNodeBindingEpochHandoff(t, locateredis.New(client), service)
		})
	}
}
