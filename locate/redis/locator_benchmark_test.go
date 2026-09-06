package redis_test

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"yola/locate"
	locateredis "yola/locate/redis"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func BenchmarkStatefulForwardRedisLookups(b *testing.B) {
	address := os.Getenv("YOLA_REDIS_INTEGRATION")
	if address == "" {
		b.Skip("set YOLA_REDIS_INTEGRATION to run Redis benchmark")
	}
	if address == "1" {
		address = "127.0.0.1:6379"
	}
	client := redis.NewClient(&redis.Options{
		Addr:     address,
		Password: os.Getenv("YOLA_REDIS_PASSWORD"),
	})
	b.Cleanup(func() { require.NoError(b, client.Close()) })
	locator := locateredis.New(client)
	ctx := context.Background()
	require.NoError(b, locator.Ping(ctx))

	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	serviceName := "yola-benchmark-" + suffix
	uid := "player-" + suffix
	nodeID := "node-" + suffix
	epoch := "epoch-" + suffix
	require.NoError(b, locator.BindNode(ctx, serviceName, uid, nodeID))
	require.NoError(b, locator.RegisterNodeEpoch(ctx, serviceName, nodeID, epoch, testTTL))
	b.Cleanup(func() {
		require.NoError(b, locator.UnbindNode(context.Background(), serviceName, uid, nodeID))
		require.NoError(b, locator.UnregisterNodeEpoch(context.Background(), serviceName, nodeID, epoch))
	})

	b.Run("serial", func(b *testing.B) {
		b.ReportAllocs()
		b.ReportMetric(3, "redis_gets/op")
		for range b.N {
			if err := statefulForwardRedisLookups(ctx, locator, serviceName, uid, nodeID); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("parallel", func(b *testing.B) {
		b.ReportAllocs()
		b.ReportMetric(3, "redis_gets/op")
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				if err := statefulForwardRedisLookups(ctx, locator, serviceName, uid, nodeID); err != nil {
					b.Error(err)
					return
				}
			}
		})
	})
}

func statefulForwardRedisLookups(ctx context.Context, locator locate.Locator, serviceName, uid, nodeID string) error {
	if _, err := locator.LocateNode(ctx, serviceName, uid); err != nil {
		return err
	}
	if _, err := locator.LocateNodeEpoch(ctx, serviceName, nodeID); err != nil {
		return err
	}
	_, err := locator.LocateNode(ctx, serviceName, uid)
	return err
}
