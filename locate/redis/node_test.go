package redis_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"os"
	"strings"
	"testing"
	"time"

	"yola/locate"
	locateredis "yola/locate/redis"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

const (
	testNodeService = "whot"
	testNodeUID     = "synthetic-player"
	testNodeTTL     = 6 * time.Hour
)

func TestNodeRenewalPreservesBindingOwnership(t *testing.T) {
	locator, server := newLocator(t)
	ctx := t.Context()
	const key = "locate:node:{d2hvdA}:c3ludGhldGljLXBsYXllcg"
	for _, nodeID := range []string{"node-a", "node-b"} {
		require.NoError(t, locator.RegisterNodeEpoch(ctx, testNodeService, nodeID, nodeID, 2*testNodeTTL))
	}
	require.ErrorIs(t, locator.RenewNode(ctx, testNodeService, testNodeUID, "node-a", "node-a"), locate.ErrNodeNotFound)
	require.NoError(t, locator.BindNode(ctx, testNodeService, testNodeUID, "node-a", "node-a"))
	server.FastForward(time.Hour)
	require.NoError(t, locator.RenewNode(ctx, testNodeService, testNodeUID, "node-a", "node-a"))
	require.Equal(t, testNodeTTL, server.TTL(key))
	require.NoError(t, locator.BindNode(ctx, testNodeService, testNodeUID, "node-b", "node-b"))
	server.FastForward(time.Hour)
	ttl := server.TTL(key)
	require.ErrorIs(t, locator.RenewNode(ctx, testNodeService, testNodeUID, "node-a", "node-a"), locate.ErrNodeConflict)
	require.ErrorIs(t, locator.RenewNode(ctx, testNodeService, testNodeUID, "node-b", "stale"), locate.ErrNodeEpochConflict)
	require.Equal(t, ttl, server.TTL(key), "rejected renewal must not extend the replacement binding")
	nodeID, err := locator.LocateNode(ctx, testNodeService, testNodeUID)
	require.NoError(t, err)
	require.Equal(t, "node-b", nodeID)

	// 原 NodeID 重启后用新 epoch 继承绑定，无需再次覆盖写入。
	require.NoError(t, locator.UnregisterNodeEpoch(ctx, testNodeService, "node-b", "node-b"))
	require.NoError(t, locator.RegisterNodeEpoch(ctx, testNodeService, "node-b", "restarted", testTTL))
	require.NoError(t, locator.RenewNode(ctx, testNodeService, testNodeUID, "node-b", "restarted"))
	require.Equal(t, testNodeTTL, server.TTL(key))
	// 显式 Bind 仍可接管；普通保活只允许使用 RenewNode。
	require.NoError(t, locator.BindNode(ctx, testNodeService, testNodeUID, "node-a", "node-a"))
	require.NoError(t, locator.UnbindNode(ctx, testNodeService, testNodeUID, "node-a", "node-a"))
	require.ErrorIs(t, locator.RenewNode(ctx, testNodeService, testNodeUID, "node-a", "node-a"), locate.ErrNodeNotFound)
	require.False(t, server.Exists(key))
}

func TestNodeRenewalRejectsInvalidArguments(t *testing.T) {
	locator, server := newLocator(t)
	for _, input := range []struct {
		service, uid, node, epoch string
		want                      error
	}{
		{service: "bad/service", uid: "uid", node: "node", epoch: "epoch", want: locate.ErrInvalidNodeBinding},
		{service: "game", node: "node", epoch: "epoch", want: locate.ErrInvalidNodeBinding},
		{service: "game", uid: "uid", epoch: "epoch", want: locate.ErrInvalidNodeBinding},
		{service: "game", uid: "uid", node: "node", want: locate.ErrInvalidNodeEpoch},
	} {
		require.ErrorIs(t, locator.RenewNode(t.Context(), input.service, input.uid, input.node, input.epoch), input.want)
	}
	require.Empty(t, server.Keys())
}

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
	require.NoError(t, locator.RegisterNodeEpoch(ctx, serviceName, nodeID, epochB, testTTL))
	require.NoError(t, locator.UnregisterNodeEpoch(ctx, serviceName, nodeID, epochA))
	located, err = locator.LocateNodeEpoch(ctx, serviceName, nodeID)
	require.NoError(t, err)
	require.Equal(t, epochB, located)
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
	require.True(t, server.Exists("locate:node:epoch:{bHVkbw}:bm9kZS1h"))
	require.True(t, server.Exists("locate:node:epoch:{d2hvdA}:bm9kZS1h"))
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

func TestNodeBindingRejectsOldEpochAndPreservesRestart(t *testing.T) {
	locator, _ := newLocator(t)
	assertNodeBindingEpochHandoff(t, locator, "game")
}

func TestNodeBindingRequiresLiveEpoch(t *testing.T) {
	for _, expired := range []bool{false, true} {
		name := "missing"
		if expired {
			name = "expired"
		}
		t.Run(name, func(t *testing.T) {
			locator, server := newLocator(t)
			ctx := t.Context()
			if expired {
				require.NoError(t, locator.RegisterNodeEpoch(ctx, "game", "node-a", "epoch-a", testTTL))
				require.NoError(t, locator.BindNode(ctx, "game", "player", "node-a", "epoch-a"))
				server.FastForward(testTTL + time.Millisecond)
			}
			require.ErrorIs(t, locator.BindNode(ctx, "game", "player", "node-a", "epoch-a"), locate.ErrNodeEpochNotFound)
			require.ErrorIs(t, locator.RenewNode(ctx, "game", "player", "node-a", "epoch-a"), locate.ErrNodeEpochNotFound)
			require.ErrorIs(t, locator.UnbindNode(ctx, "game", "player", "node-a", "epoch-a"), locate.ErrNodeEpochNotFound)
			nodeID, err := locator.LocateNode(ctx, "game", "player")
			if !expired {
				require.ErrorIs(t, err, locate.ErrNodeNotFound)
				return
			}
			require.NoError(t, err)
			require.Equal(t, "node-a", nodeID)
			require.Equal(t, testNodeTTL-testTTL-time.Millisecond, server.TTL("locate:node:{Z2FtZQ}:cGxheWVy"))
		})
	}
}

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

func TestNodeKeysShareServiceHashTag(t *testing.T) {
	locator, server := newLocator(t)
	ctx := t.Context()
	const service = "game-a"
	for _, nodeID := range []string{"node-a", "node:{}\x00/节点"} {
		require.NoError(t, locator.RegisterNodeEpoch(ctx, service, nodeID, "epoch", testTTL))
	}
	for _, uid := range []string{"player-a", "player:{}\x00/玩家", "node-a"} {
		require.NoError(t, locator.BindNode(ctx, service, uid, "node-a", "epoch"))
	}
	require.Len(t, server.Keys(), 5)
	for _, key := range server.Keys() {
		_, suffix, found := strings.Cut(key, "{")
		require.True(t, found)
		tag, _, found := strings.Cut(suffix, "}")
		require.True(t, found)
		require.Equal(t, "Z2FtZS1h", tag)
	}
	require.NoError(t, locator.RegisterNodeEpoch(ctx, "game-b", "node-a", "epoch", testTTL))
	require.NoError(t, locator.BindNode(ctx, "game-b", "player-a", "node-a", "epoch"))
	require.Len(t, server.Keys(), 7)
	require.True(t, server.Exists("locate:node:{Z2FtZS1i}:cGxheWVyLWE"))
	require.True(t, server.Exists("locate:node:epoch:{Z2FtZS1i}:bm9kZS1h"))
}

// assertNodeBindingEpochHandoff 在替身和真实 Redis 上验证相同的修改权与重启契约。
func assertNodeBindingEpochHandoff(t *testing.T, locator locate.Locator, service string) {
	t.Helper()
	ctx := t.Context()
	const uid = "player"
	require.NoError(t, locator.RegisterNodeEpoch(ctx, service, "node-a", "old", testTTL))
	require.NoError(t, locator.RegisterNodeEpoch(ctx, service, "node-b", "other", testTTL))
	require.NoError(t, locator.BindNode(ctx, service, uid, "node-a", "old"))
	require.NoError(t, locator.BindNode(ctx, service, uid, "node-b", "other"))
	require.ErrorIs(t, locator.RenewNode(ctx, service, uid, "node-a", "old"), locate.ErrNodeConflict)
	require.NoError(t, locator.RenewNode(ctx, service, uid, "node-b", "other"))
	require.NoError(t, locator.UnbindNode(ctx, service, uid, "node-a", "old"))
	nodeID, err := locator.LocateNode(ctx, service, uid)
	require.NoError(t, err)
	require.Equal(t, "node-b", nodeID)

	require.NoError(t, locator.UnregisterNodeEpoch(ctx, service, "node-a", "old"))
	require.NoError(t, locator.RegisterNodeEpoch(ctx, service, "node-a", "new", testTTL))
	require.ErrorIs(t, locator.BindNode(ctx, service, uid, "node-a", "old"), locate.ErrNodeEpochConflict)
	require.ErrorIs(t, locator.RenewNode(ctx, service, uid, "node-a", "old"), locate.ErrNodeEpochConflict)
	require.ErrorIs(t, locator.UnbindNode(ctx, service, uid, "node-a", "old"), locate.ErrNodeEpochConflict)
	nodeID, err = locator.LocateNode(ctx, service, uid)
	require.NoError(t, err)
	require.Equal(t, "node-b", nodeID)

	require.NoError(t, locator.BindNode(ctx, service, uid, "node-a", "new"))
	require.ErrorIs(t, locator.UnbindNode(ctx, service, uid, "node-a", "old"), locate.ErrNodeEpochConflict)
	nodeID, err = locator.LocateNode(ctx, service, uid)
	require.NoError(t, err)
	require.Equal(t, "node-a", nodeID)

	require.NoError(t, locator.UnregisterNodeEpoch(ctx, service, "node-a", "new"))
	nodeID, err = locator.LocateNode(ctx, service, uid)
	require.NoError(t, err)
	require.Equal(t, "node-a", nodeID)
	require.ErrorIs(t, locator.BindNode(ctx, service, uid, "node-a", "new"), locate.ErrNodeEpochNotFound)
	require.ErrorIs(t, locator.UnbindNode(ctx, service, uid, "node-a", "new"), locate.ErrNodeEpochNotFound)
	require.NoError(t, locator.RegisterNodeEpoch(ctx, service, "node-a", "restarted", testTTL))
	require.NoError(t, locator.RenewNode(ctx, service, uid, "node-a", "restarted"))
	nodeID, err = locator.LocateNode(ctx, service, uid)
	require.NoError(t, err)
	require.Equal(t, "node-a", nodeID)
	require.NoError(t, locator.UnbindNode(ctx, service, uid, "node-a", "restarted"))
	require.NoError(t, locator.UnbindNode(ctx, service, uid, "node-a", "restarted"))
	require.ErrorIs(t, locator.UnbindNode(ctx, service, uid, "node-a", "old"), locate.ErrNodeEpochConflict)
	_, err = locator.LocateNode(ctx, service, uid)
	require.ErrorIs(t, err, locate.ErrNodeNotFound)
	require.ErrorIs(t, locator.RenewNode(ctx, service, uid, "node-a", "restarted"), locate.ErrNodeNotFound)

	// 相同 NodeID/UID 在其他 service 中不能借用本 service 的 epoch。
	require.ErrorIs(t, locator.BindNode(ctx, service+"-other", uid, "node-a", "restarted"), locate.ErrNodeEpochNotFound)
}

func nodeBindingTestKeys(service string) []string {
	tag := "{" + base64.RawURLEncoding.EncodeToString([]byte(service)) + "}:"
	return []string{
		"locate:node:" + tag + base64.RawURLEncoding.EncodeToString([]byte("player")),
		"locate:node:epoch:" + tag + base64.RawURLEncoding.EncodeToString([]byte("node-a")),
		"locate:node:epoch:" + tag + base64.RawURLEncoding.EncodeToString([]byte("node-b")),
	}
}
