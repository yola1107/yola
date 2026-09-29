package redis_test

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"maps"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"yola/locate"
	locateredis "yola/locate/redis"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

const (
	clusterTransitionPrimaryCount = 3
	clusterTransitionNodeCount    = 6
	clusterTransitionTimeout      = 20 * time.Second
	clusterTransitionPoll         = 50 * time.Millisecond
	clusterTransitionEpochTTL     = 3 * time.Minute
)

func TestNodeBindingClusterMigration(t *testing.T) {
	client, shards := clusterTransitionClients(t)
	service, slot, sourceIndex := emptyClusterTransitionSlot(t, client, shards)
	source := shards[sourceIndex]
	target := shards[(sourceIndex+1)%len(shards)]
	keys := nodeBindingTestKeys(service)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), clusterTransitionTimeout)
		defer cancel()
		if err := restoreClusterTransitionSlot(ctx, shards, sourceIndex, slot, keys); err != nil {
			t.Errorf("restore migration slot %d: %v", slot, err)
		}
	})
	ctx := t.Context()
	store := locateredis.New(client)
	require.NoError(t, store.RegisterNodeEpoch(ctx, service, "node-a", "current", clusterTransitionEpochTTL))
	require.NoError(t, store.BindNode(ctx, service, "player", "node-a", "current"))
	require.NoError(t, target.primary.Do(ctx, "CLUSTER", "SETSLOT", slot, "IMPORTING", source.primaryID).Err())
	require.NoError(t, source.primary.Do(ctx, "CLUSTER", "SETSLOT", slot, "MIGRATING", target.primaryID).Err())
	require.NoError(t, migrateClusterTransitionKey(ctx, source.primary, target.primary, keys[1]))

	// Redis 用 TRYAGAIN 拒绝分别位于源、目标节点的同 slot 多 key Lua。
	err := store.BindNode(ctx, service, "player", "node-a", "current")
	require.True(t, redis.HasErrorPrefix(err, "TRYAGAIN"), "partial migration Bind: %v", err)
	err = store.UnbindNode(ctx, service, "player", "node-a", "current")
	require.True(t, redis.HasErrorPrefix(err, "TRYAGAIN"), "partial migration Unbind: %v", err)
	value, err := source.primary.Get(ctx, keys[0]).Result()
	require.NoError(t, err)
	require.Equal(t, "node-a", value)
	err = source.primary.Get(ctx, keys[1]).Err()
	require.True(t, redis.HasErrorPrefix(err, "ASK"), "migrated epoch lookup: %v", err)
	epoch, err := store.LocateNodeEpoch(ctx, service, "node-a")
	require.NoError(t, err)
	require.Equal(t, "current", epoch)

	require.NoError(t, migrateClusterTransitionKey(ctx, source.primary, target.primary, keys[0]))
	// slot 尚未发布新 owner，两个 key 均在 IMPORTING 目标时验证 ASK 重试。
	require.NoError(t, store.BindNode(ctx, service, "player", "node-a", "current"))
	require.ErrorIs(t, store.UnbindNode(ctx, service, "player", "node-a", "old"), locate.ErrNodeEpochConflict)
	require.NoError(t, target.primary.Do(ctx, "CLUSTER", "SETSLOT", slot, "NODE", target.primaryID).Err())
	for _, shard := range shards {
		if shard.primaryID != target.primaryID {
			require.NoError(t, shard.primary.Do(ctx, "CLUSTER", "SETSLOT", slot, "NODE", target.primaryID).Err())
		}
	}
	transitionCtx, cancel := context.WithTimeout(ctx, clusterTransitionTimeout)
	_, err = waitClusterTransitionView(transitionCtx, shards, slot, target.primaryID, false)
	cancel()
	require.NoError(t, err)
	err = source.primary.Get(ctx, keys[0]).Err()
	require.True(t, redis.HasErrorPrefix(err, "MOVED"), "completed migration lookup: %v", err)
	require.NoError(t, store.BindNode(ctx, service, "player", "node-a", "current"))
	require.ErrorIs(t, store.BindNode(ctx, service, "player", "node-a", "old"), locate.ErrNodeEpochConflict)
	require.ErrorIs(t, store.UnbindNode(ctx, service, "player", "node-a", "old"), locate.ErrNodeEpochConflict)
	value, err = store.LocateNode(ctx, service, "player")
	require.NoError(t, err)
	require.Equal(t, "node-a", value)
	epoch, err = store.LocateNodeEpoch(ctx, service, "node-a")
	require.NoError(t, err)
	require.Equal(t, "current", epoch)
	t.Logf("slot %d: partial Lua rejected; ASK/MOVED recovered; old epoch rejected", slot)
}

func TestNodeBindingClusterCooperativeFailover(t *testing.T) {
	client, shards := clusterTransitionClients(t)
	// FAILOVER 会交换整个 shard 的主从角色，只在专用空集群上开始。
	for _, shard := range shards {
		size, err := shard.primary.DBSize(t.Context()).Result()
		require.NoError(t, err)
		require.Zero(t, size, "cooperative failover requires an empty dedicated cluster")
	}
	service, slot, sourceIndex := emptyClusterTransitionSlot(t, client, shards)
	source := shards[sourceIndex]
	keys := nodeBindingTestKeys(service)
	marker := keys[0] + ":replication-marker"
	keys = append(keys, marker)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*clusterTransitionTimeout)
		defer cancel()
		view, restoreErr := waitClusterTransitionView(ctx, shards, slot, "", false)
		if restoreErr == nil {
			switch view.owner {
			case source.primaryID:
				// 命令接受不代表切换完成；原主仍正确时不发起额外 FAILOVER。
			case source.replicaID:
				restoreErr = acknowledgeClusterTransitionMarker(ctx, source.replica, source.primary, marker)
				if restoreErr == nil {
					restoreErr = source.primary.ClusterFailover(ctx).Err()
				}
				if restoreErr == nil {
					_, restoreErr = waitClusterTransitionView(ctx, shards, slot, source.primaryID, false)
				}
			default:
				restoreErr = fmt.Errorf("slot %d has an unexpected owner %s; topology not reset", slot, view.owner)
			}
		}
		if restoreErr == nil {
			_, restoreErr = waitClusterTransitionReplication(ctx, source.primary, source.replica)
		}
		if restoreErr != nil {
			t.Errorf("restore original primary for slot %d: %v", slot, restoreErr)
			cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), clusterTransitionTimeout)
			defer cancelCleanup()
			// 恢复失败只清理预先登记的任务 key，不再改 slot 或其他 shard。
			if err := client.Del(cleanupCtx, keys...).Err(); err != nil {
				t.Errorf("remove failover test keys: %v", err)
			}
			return
		}
		if err := restoreClusterTransitionSlot(ctx, shards, sourceIndex, slot, keys); err != nil {
			t.Errorf("clean restored failover slot %d: %v", slot, err)
			return
		}
		if _, err := waitClusterTransitionReplication(ctx, source.primary, source.replica); err != nil {
			t.Errorf("replication after failover cleanup: %v", err)
		}
	})
	ctx := t.Context()
	store := locateredis.New(client)
	require.NoError(t, store.RegisterNodeEpoch(ctx, service, "node-a", "current", clusterTransitionEpochTTL))
	require.NoError(t, store.BindNode(ctx, service, "player", "node-a", "current"))
	require.NoError(t, acknowledgeClusterTransitionMarker(ctx, source.primary, source.replica, marker), "replication before failover")
	require.NoError(t, source.replica.ClusterFailover(ctx).Err())
	transitionCtx, cancel := context.WithTimeout(ctx, clusterTransitionTimeout)
	_, err := waitClusterTransitionView(transitionCtx, shards, slot, source.replicaID, false)
	cancel()
	require.NoError(t, err)
	err = source.primary.Get(ctx, keys[0]).Err()
	require.True(t, redis.HasErrorPrefix(err, "MOVED"), "demoted primary lookup: %v", err)
	value, err := store.LocateNode(ctx, service, "player")
	require.NoError(t, err)
	require.Equal(t, "node-a", value)
	epoch, err := store.LocateNodeEpoch(ctx, service, "node-a")
	require.NoError(t, err)
	require.Equal(t, "current", epoch)
	require.ErrorIs(t, store.BindNode(ctx, service, "player", "node-a", "old"), locate.ErrNodeEpochConflict)
	require.ErrorIs(t, store.UnbindNode(ctx, service, "player", "node-a", "old"), locate.ErrNodeEpochConflict)
	require.NoError(t, store.BindNode(ctx, service, "player", "node-a", "current"))
	t.Logf("slot %d: SET+WAIT confirmed before cooperative FAILOVER; this does not test asynchronous write loss", slot)
}

type clusterTransitionShard struct {
	slots     []redis.SlotRange
	primaryID string
	primary   *redis.Client
	replicaID string
	replica   *redis.Client
}

func clusterTransitionClients(t *testing.T) (*redis.ClusterClient, []clusterTransitionShard) {
	t.Helper()
	addresses := os.Getenv("YOLA_REDIS_CLUSTER_INTEGRATION")
	if addresses == "" || os.Getenv("YOLA_REDIS_CLUSTER_ADMIN_INTEGRATION") != "1" {
		t.Skip("set all six dedicated Redis addresses and YOLA_REDIS_CLUSTER_ADMIN_INTEGRATION=1 for topology changes")
	}
	allowed := strings.Split(addresses, ",")
	require.Len(t, allowed, clusterTransitionNodeCount)
	for index, address := range allowed {
		allowed[index] = strings.TrimSpace(address)
		_, _, err := net.SplitHostPort(allowed[index])
		require.NoError(t, err)
	}
	password := os.Getenv("YOLA_REDIS_PASSWORD")
	client := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs: allowed, Password: password, MaxRedirects: 4, MaxRetries: -1, ContextTimeoutEnabled: true,
	})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	shards, err := client.ClusterShards(t.Context()).Result()
	require.NoError(t, err)
	require.Len(t, shards, clusterTransitionPrimaryCount)
	result := make([]clusterTransitionShard, 0, len(shards))
	for _, shard := range shards {
		require.Len(t, shard.Nodes, 2)
		entry := clusterTransitionShard{slots: shard.Slots}
		for _, node := range shard.Nodes {
			require.Equal(t, "online", node.Health)
			address := net.JoinHostPort(node.IP, strconv.FormatInt(node.Port, 10))
			require.Contains(t, allowed, address, "all advertised nodes must be explicitly authorized")
			direct := redis.NewClient(&redis.Options{
				Addr: address, Password: password, MaxRetries: -1, ContextTimeoutEnabled: true,
			})
			t.Cleanup(func() { require.NoError(t, direct.Close()) })
			switch node.Role {
			case "master":
				entry.primaryID, entry.primary = node.ID, direct
			case "replica":
				entry.replicaID, entry.replica = node.ID, direct
			default:
				t.Fatalf("unexpected cluster role %q", node.Role)
			}
		}
		require.NotNil(t, entry.primary)
		require.NotNil(t, entry.replica)
		nodes, nodesErr := entry.primary.ClusterNodes(t.Context()).Result()
		require.NoError(t, nodesErr)
		for _, line := range strings.Split(nodes, "\n") {
			fields := strings.Fields(line)
			if len(fields) > 8 {
				for _, slotState := range fields[8:] {
					require.False(t, strings.HasPrefix(slotState, "["), "cluster already has a slot transition")
				}
			}
		}
		result = append(result, entry)
	}
	return client, result
}

func emptyClusterTransitionSlot(t *testing.T, client *redis.ClusterClient, shards []clusterTransitionShard) (string, int, int) {
	t.Helper()
	for range 16 {
		service := "yola-i48-transition-" + rand.Text()
		slot, err := client.ClusterKeySlot(t.Context(), nodeBindingTestKeys(service)[0]).Result()
		require.NoError(t, err)
		owner := -1
		empty := true
		for index, shard := range shards {
			count, countErr := shard.primary.ClusterCountKeysInSlot(t.Context(), int(slot)).Result()
			require.NoError(t, countErr)
			empty = empty && count == 0
			for _, interval := range shard.slots {
				if slot >= interval.Start && slot <= interval.End {
					owner = index
				}
			}
		}
		require.NotEqual(t, -1, owner)
		if empty {
			ctx, cancel := context.WithTimeout(t.Context(), clusterTransitionTimeout)
			_, err = waitClusterTransitionView(ctx, shards, int(slot), shards[owner].primaryID, false)
			cancel()
			require.NoError(t, err)
			return service, int(slot), owner
		}
	}
	t.Fatal("no empty slot found; topology was not changed")
	return "", 0, 0
}

func migrateClusterTransitionKey(ctx context.Context, source, target *redis.Client, key string) error {
	host, port, err := net.SplitHostPort(target.Options().Addr)
	if err != nil {
		return err
	}
	args := []any{"MIGRATE", host, port, key, 0, 5000}
	if password := target.Options().Password; password != "" {
		args = append(args, "AUTH", password)
	}
	return source.Do(ctx, args...).Err()
}

// restoreClusterTransitionSlot 先确认 slot 中只有本测试 key，再删除并恢复原 owner。
func restoreClusterTransitionSlot(ctx context.Context, shards []clusterTransitionShard, sourceIndex, slot int, keys []string) error {
	present := make([][]string, len(shards))
	for index, shard := range shards {
		found, err := shard.primary.ClusterGetKeysInSlot(ctx, slot, len(keys)+1).Result()
		if err != nil {
			return err
		}
		for _, key := range found {
			if !slices.Contains(keys, key) {
				return fmt.Errorf("slot %d contains an unowned key; no cleanup or topology reset performed", slot)
			}
		}
		present[index] = found
	}
	var cleanupErr error
	for index, shard := range shards {
		if len(present[index]) == 0 {
			continue
		}
		conn := shard.primary.Conn()
		err := conn.Do(ctx, "ASKING").Err()
		if err == nil {
			err = conn.Del(ctx, present[index]...).Err()
		}
		cleanupErr = errors.Join(cleanupErr, err, conn.Close())
	}
	if cleanupErr != nil {
		return cleanupErr
	}
	for _, shard := range shards {
		count, err := shard.primary.ClusterCountKeysInSlot(ctx, slot).Result()
		if err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("slot %d changed during cleanup; topology not reset", slot)
		}
	}
	source := shards[sourceIndex]
	view, err := waitClusterTransitionView(ctx, shards, slot, "", true)
	if err != nil {
		return err
	}
	if view.transition {
		for _, shard := range shards {
			cleanupErr = errors.Join(cleanupErr, shard.primary.Do(ctx, "CLUSTER", "SETSLOT", slot, "STABLE").Err())
		}
		if cleanupErr != nil {
			return cleanupErr
		}
	}
	if view.owner == source.primaryID {
		_, err = waitClusterTransitionView(ctx, shards, slot, source.primaryID, false)
		return err
	}
	var current *redis.Client
	for _, shard := range shards {
		if shard.primaryID == view.owner {
			current = shard.primary
			break
		}
	}
	if current == nil {
		return fmt.Errorf("slot %d owner is not an original primary; topology not reset", slot)
	}
	// 空 slot 也按反向导入完成交接；先 STABLE 再 NODE 不会提升原主的 configEpoch。
	if err = source.primary.Do(ctx, "CLUSTER", "SETSLOT", slot, "IMPORTING", view.owner).Err(); err != nil {
		return err
	}
	if err = current.Do(ctx, "CLUSTER", "SETSLOT", slot, "MIGRATING", source.primaryID).Err(); err != nil {
		return err
	}
	if err = source.primary.Do(ctx, "CLUSTER", "SETSLOT", slot, "NODE", source.primaryID).Err(); err != nil {
		return err
	}
	for _, shard := range shards {
		if shard.primaryID != source.primaryID {
			cleanupErr = errors.Join(cleanupErr, shard.primary.Do(ctx, "CLUSTER", "SETSLOT", slot, "NODE", source.primaryID).Err())
		}
	}
	if cleanupErr != nil {
		return cleanupErr
	}
	_, err = waitClusterTransitionView(ctx, shards, slot, source.primaryID, false)
	return err
}

// acknowledgeClusterTransitionMarker 在同一复制代次内用 SET/WAIT 确认写入，再复核复制链路。
func acknowledgeClusterTransitionMarker(ctx context.Context, primary, replica *redis.Client, marker string) error {
	ctx, cancel := context.WithTimeout(ctx, clusterTransitionTimeout)
	defer cancel()
	for {
		replID, err := waitClusterTransitionReplication(ctx, primary, replica)
		if err != nil {
			return err
		}
		conn := primary.Conn()
		err = conn.Set(ctx, marker, "confirmed", clusterTransitionEpochTTL).Err()
		if err == nil {
			var acknowledged int64
			acknowledged, err = conn.Wait(ctx, 1, 5*time.Second).Result()
			if err == nil && acknowledged < 1 {
				err = errors.New("replica did not acknowledge the marker")
			}
		}
		if err = errors.Join(err, conn.Close()); err != nil {
			return err
		}
		currentID, err := waitClusterTransitionReplication(ctx, primary, replica)
		if err != nil {
			return err
		}
		if currentID == replID {
			return nil
		}
		// 复制代次变化只重新确认任务 marker，不重发 FAILOVER。
	}
}

func waitClusterTransitionReplication(ctx context.Context, primary, replica *redis.Client) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, clusterTransitionTimeout)
	defer cancel()
	ticker := time.NewTicker(clusterTransitionPoll)
	defer ticker.Stop()
	for {
		replID, err := checkClusterTransitionReplication(ctx, primary, replica)
		if err == nil {
			return replID, nil
		}
		select {
		case <-ctx.Done():
			return "", errors.Join(ctx.Err(), err)
		case <-ticker.C:
		}
	}
}

func checkClusterTransitionReplication(ctx context.Context, primary, replica *redis.Client) (string, error) {
	before, err := clusterTransitionReplicationInfo(ctx, primary)
	if err != nil {
		return "", err
	}
	if before["role"] != "master" || before["connected_slaves"] != "1" || before["master_replid"] == "" {
		return "", errors.New("expected primary and its sole replica are not ready")
	}
	peer := make(map[string]string)
	for _, field := range strings.Split(before["slave0"], ",") {
		key, value, found := strings.Cut(field, "=")
		if found {
			peer[key] = value
		}
	}
	if peer["state"] != "online" || net.JoinHostPort(peer["ip"], peer["port"]) != replica.Options().Addr {
		return "", errors.New("primary has not confirmed the expected replica as online")
	}
	catchupOffset, err := strconv.ParseInt(before["master_repl_offset"], 10, 64)
	if err != nil {
		return "", err
	}
	current, err := clusterTransitionReplicationInfo(ctx, replica)
	if err != nil {
		return "", err
	}
	if current["role"] != "slave" || current["master_link_status"] != "up" || current["master_sync_in_progress"] != "0" ||
		net.JoinHostPort(current["master_host"], current["master_port"]) != primary.Options().Addr {
		return "", errors.New("replica link or synchronization is not ready for the expected primary")
	}
	if current["master_replid"] != before["master_replid"] {
		return "", errors.New("primary and replica replication IDs differ")
	}
	// 捕获主端下界后检查已应用 offset，不把 read offset 当作复制完成。
	applied, err := strconv.ParseInt(current["slave_repl_offset"], 10, 64)
	if err != nil {
		return "", err
	}
	if applied < catchupOffset {
		return "", fmt.Errorf("replica applied offset %d is behind captured primary offset %d", applied, catchupOffset)
	}
	after, err := clusterTransitionReplicationInfo(ctx, primary)
	if err != nil {
		return "", err
	}
	if after["role"] != "master" || after["connected_slaves"] != "1" || after["master_replid"] != before["master_replid"] {
		return "", errors.New("primary role or replication ID changed during readiness check")
	}
	return before["master_replid"], nil
}

func clusterTransitionReplicationInfo(ctx context.Context, client *redis.Client) (map[string]string, error) {
	raw, err := client.Info(ctx, "replication").Result()
	if err != nil {
		return nil, err
	}
	info := make(map[string]string)
	for _, line := range strings.Split(raw, "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), ":")
		if found {
			info[key] = value
		}
	}
	return info, nil
}

type clusterTransitionIdentity struct {
	role      string
	primaryID string
	epoch     uint64
}

type clusterTransitionView struct {
	nodes      map[string]clusterTransitionIdentity
	owner      string
	transition bool
}

// waitClusterTransitionView 比较全部主、从节点的 owner、角色和 configEpoch；WAIT 不覆盖这些集群总线状态。
func waitClusterTransitionView(
	ctx context.Context, shards []clusterTransitionShard, slot int, ownerID string, allowTransition bool,
) (clusterTransitionView, error) {

	observers := make([]*redis.Client, 0, clusterTransitionNodeCount)
	for _, shard := range shards {
		observers = append(observers, shard.primary, shard.replica)
	}
	ticker := time.NewTicker(clusterTransitionPoll)
	defer ticker.Stop()
	var lastErr error
	for {
		var reference clusterTransitionView
		lastErr = nil
		for index, observer := range observers {
			view, err := readClusterTransitionView(ctx, observer, slot)
			if err != nil {
				lastErr = err
				break
			}
			if ownerID != "" && view.owner != ownerID {
				lastErr = fmt.Errorf("%s reports slot %d owner %s, want %s", observer.Options().Addr, slot, view.owner, ownerID)
				break
			}
			if index == 0 {
				reference = view
			} else if view.owner != reference.owner || !maps.Equal(view.nodes, reference.nodes) {
				lastErr = fmt.Errorf("%s has not converged on slot %d owner, roles and epochs", observer.Options().Addr, slot)
				break
			}
			reference.transition = reference.transition || view.transition
		}
		if lastErr == nil {
			if allowTransition || !reference.transition {
				return reference, nil
			}
			lastErr = fmt.Errorf("slot %d still has a migration or import marker", slot)
		}
		select {
		case <-ctx.Done():
			return clusterTransitionView{}, errors.Join(ctx.Err(), lastErr)
		case <-ticker.C:
		}
	}
}

func readClusterTransitionView(ctx context.Context, observer *redis.Client, slot int) (clusterTransitionView, error) {
	raw, err := observer.ClusterNodes(ctx).Result()
	if err != nil {
		return clusterTransitionView{}, err
	}
	view := clusterTransitionView{nodes: make(map[string]clusterTransitionIdentity, clusterTransitionNodeCount)}
	primaryCount := 0
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 8 || fields[7] != "connected" {
			return clusterTransitionView{}, errors.New("cluster node record is incomplete or disconnected")
		}
		flags := strings.Split(fields[2], ",")
		if slices.Contains(flags, "fail") || slices.Contains(flags, "fail?") || slices.Contains(flags, "handshake") {
			return clusterTransitionView{}, errors.New("cluster node is not healthy")
		}
		role := "slave"
		if slices.Contains(flags, "master") {
			role = "master"
			primaryCount++
		} else if !slices.Contains(flags, "slave") {
			return clusterTransitionView{}, errors.New("cluster node has no primary or replica role")
		}
		epoch, epochErr := strconv.ParseUint(fields[6], 10, 64)
		if epochErr != nil {
			return clusterTransitionView{}, epochErr
		}
		view.nodes[fields[0]] = clusterTransitionIdentity{role: role, primaryID: fields[3], epoch: epoch}
		for _, interval := range fields[8:] {
			if strings.HasPrefix(interval, "[") {
				if !strings.HasPrefix(interval, fmt.Sprintf("[%d-", slot)) {
					return clusterTransitionView{}, errors.New("another slot has an active transition")
				}
				view.transition = true
				continue
			}
			first, last, ranged := strings.Cut(interval, "-")
			start, startErr := strconv.Atoi(first)
			if startErr != nil {
				return clusterTransitionView{}, startErr
			}
			end := start
			if ranged {
				end, err = strconv.Atoi(last)
				if err != nil {
					return clusterTransitionView{}, err
				}
			}
			if slot >= start && slot <= end {
				if role != "master" || view.owner != "" {
					return clusterTransitionView{}, errors.New("slot ownership is ambiguous")
				}
				view.owner = fields[0]
			}
		}
	}
	if len(view.nodes) != clusterTransitionNodeCount || primaryCount != clusterTransitionPrimaryCount || view.owner == "" {
		return clusterTransitionView{}, errors.New("cluster roles or slot owner are not ready")
	}
	for _, node := range view.nodes {
		if node.role == "slave" && view.nodes[node.primaryID].role != "master" {
			return clusterTransitionView{}, errors.New("replica primary is not ready")
		}
	}
	return view, nil
}
