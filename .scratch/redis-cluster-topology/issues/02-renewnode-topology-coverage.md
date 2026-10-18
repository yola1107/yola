# 02：补齐 RenewNode 拓扑管理覆盖

Status: resolved
Blocked by: 01

沿现有真实 Cluster 夹具，通过公开 NodeLocator 接口补齐条件续租契约。验收条件见[任务范围](../spec.md#验收)。

- [x] 迁移分裂窗口中的续租被拒绝且保留原绑定。
- [x] ASK/MOVED 恢复后及切主后可以成功续租。
- [x] 旧 epoch、绑定改属或缺失时不覆盖归属、延长错误绑定或创建绑定。
- [x] 真实六节点 race 回归与相关包级检查通过。
- [x] 单独切主与迁移、切主组合各十轮 race 回归通过。

## Comments

先稳定切主回归，再新增拓扑场景；普通续租和基础 Cluster 回归已有实现，继续复用。

2026-10-17：已新增迁移分裂窗口的 TRYAGAIN 与 TTL 保持、ASK/MOVED 恢复后的成功续租、旧 epoch 与改绑拒绝、缺失绑定不创建，以及切主后相同契约的回归。ASK 导入阶段的缺失绑定允许 ASK/TRYAGAIN，以目标节点的 ASKING EXISTS 独立确认未创建；稳定阶段断言 ErrNodeNotFound。

变异验证仅通过临时 Go overlay 执行，生产文件未改：删除续租的 PEXPIRE 后，新迁移用例因 TTL 仍约 60s 失败；将续租脚本替换为 Bind 后，因改绑场景应返回冲突却返回 nil 而失败。正常实现的迁移、连续切主组合一轮通过。

验证环境：Go 1.26.7 darwin/arm64，Docker Redis 7.4.9；每套专用集群含三主三从，六进程同容器，cluster-node-timeout 为 5000ms。两组分别使用 127.0.0.1:17640–17645 和 127.0.0.1:17700–17705，开始前逐节点确认 online 和复制就绪。

十轮验收全部通过：单独切主十轮，包报告耗时 283.029s；组合中的迁移十轮、切主十轮，包报告耗时 283.433s。每个切主用例还在同一分片执行两轮往返。两组均无 race 报告或清理错误，专用容器均已删除。命令在根 module 执行，由环境注入对应六个地址及 `YOLA_REDIS_CLUSTER_ADMIN_INTEGRATION=1`，清除 `YOLA_REDIS_PASSWORD`：

```sh
go test -race -count=10 -timeout=600s -run '^TestNodeBindingClusterCooperativeFailover$' -v ./locate/redis
go test -race -count=10 -timeout=600s -run '^TestNodeBindingCluster(Migration|CooperativeFailover)$' -v ./locate/redis
```

已通过的包级检查：清除三个 Redis 集成开关后执行 `go test -race -count=1 -timeout=180s ./locate/redis`，以及 `golangci-lint run ./locate/redis`、`git diff --check`。包级运行未把跳过的外部场景当成通过。

两组管理回归对应的测试文件 SHA-256 均为 `fd5508501a5db575593c25228f27e03b4f97a3e18a9b8e59a2231e2aa5a87bf9`。Standards 与 Spec 双轴复审均无发现；任务改动未提交。

结论限于上述本地专用集群和所运行的场景；未检查独立 test module、未运行全仓 make check/race、容量基准或生产部署验收。本次没有协议、生产实现或生成文件改动。

复盘候选：现有 Makefile 已有 check/race，但管理用例需要显式地址和 ADMIN 开关。后续可提供专用集群验收入口；当前先在本票保留实际环境、命令与执行结果。
