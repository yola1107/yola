# 01：修复协作切主回归间歇超时

Status: resolved
Blocked by: None

在专用六节点集群中定位并修复切主后的拓扑等待超时。已有复现：迁移与切主组合三轮失败两轮，切主单独三轮失败一轮。验收条件见[任务范围](../spec.md#验收)。

- [x] 根因有可验证证据，回归能够检出原问题。
- [x] 同分片连续切主回归在修复前失败、修复后通过。
- [x] 原拓扑及任务 key 正确清理。

## Comments

实施基线：`d0a10a634b9475ae60cef45623777426a8d02a40`。复用上一轮本地 Docker Redis 7.4.9 的失败证据；生产实现缺陷尚未成立。

2026-10-17：同一分片固定复现确认，第二次切主沿用旧选举 epoch，短暂晋升后退回原主。六节点随后一致认定原主仍持有槽；不是 Locator 读写断言失败。依据为 Redis 7.4.9 的 [clusterHandleSlaveFailover](https://github.com/redis/redis/blob/7.4.9/src/cluster_legacy.c#L4212) 与 [resetManualFailover](https://github.com/redis/redis/blob/7.4.9/src/cluster_legacy.c#L4529)；前者保留选举重试状态，后者不清除此状态。

回归改为同一分片连续往返。修复前 `go test -race -count=1 -timeout=300s -run '^TestNodeBindingClusterCooperativeFailover$' -v ./locate/redis` 在第二轮失败，修复后通过。夹具按各节点配置计算预算，过期尝试只在原拓扑恢复且 marker 再次确认后重试，并要求新主 epoch 提升。清理无错误，测试专用容器已删除。

Standards 与 Spec 双轴复审均无阻断问题。任务范围不变，两组十轮 race 验收在 02 补齐覆盖后统一执行并记录，避免重复运行长测；全局验收仍见 spec。
