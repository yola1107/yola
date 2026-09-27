# 性能验证

本页保留成本模型和复测入口，不给尚未定义负载与 SLO 的首版承诺容量。已提交的历史测量可由 Git 追溯；建立新基线时记录 commit、工具、配置、机器、拓扑和负载。

## 热路径成本

已绑定 Stateful 请求依次执行 Gateway 查 Node binding、查 epoch、gRPC Forward、Node 再查 binding。正常 GET 速率约为 `3 × 已绑定 Stateful QPS + 未绑定 Stateful QPS`；Stateless 不查 Node Locator，认证、续租和 Push 另计。Node 复查承担 fencing，不能为省 I/O 删除。

Gate lease 默认60s，心跳在剩余不超过30s时续租；健康时约为 `在线连接数 / 30s` 次脚本/秒。Node binding 的 RenewNode 由业务 owner 调度，不加入每条请求的固定成本。

| 成本项 | 口径 |
| --- | --- |
| TCP 读写 buffer | 各4096+4B，每连接约8KiB，不含 socket、Session、队列和栈 |
| WebSocket buffer | 每连接4KiB read buffer；write buffer 从 pool 借用 |
| 认证后的业务 FIFO | 每个 HeartbeatHandler 连接一个 worker，默认等待8帧，不含当前请求 |
| NATS 积压 | 订阅数 × 队列容量 × broker 实际 payload 上限，加 runtime 开销 |
| 业务同步 Push | LocateGate 与 Gateway RPC 占用调用方 worker；多次 Push 分别计时 |

这些是结构成本，不能换算为实际 RSS 或安全容量。只有当前 benchmark/profile 指出瓶颈后，才决定缓存、队列或并发调整。

<a id="nats-capacity"></a>
## NATS 容量口径

WithMaxPayloadBytes 限制发布和接收出队后的 payload，不能限制已经进入订阅队列的大消息。broker 上限64KiB、队列256时，单订阅仅 payload 可约16MiB；broker 放行1MiB时可达256MiB。需同时固定 broker max_payload、订阅数和队列容量。

关闭后释放引用不保证 RSS 立即下降。RSS 区分接收进程、发布进程与 broker；drop 区分本地队列拒绝、出队丢弃和未观测到的传输损失。[实现边界](./eventbus.md#4-nats-生命周期)、[基准](../event/nats/capacity_benchmark_test.go)

## 容量验收

1. 固定目标连接数、消息模型、业务 SLO、commit、机器、独立进程拓扑、TLS 和超时预算；使用[任务专用依赖](./README.md#开发与验证)。正式计时不并行编译或其他压测。
2. 从小规模逐档增加负载，先排除压测端、单 IP 连接上限或网络出口成为瓶颈。分开测登录突发、稳态、慢连接、重连和故障窗口。
3. 排除预热，记录 CPU、RSS、GC、goroutine、FD、队列等待/拒绝、p99，以及 Redis/etcd/NATS 的延迟和错误；核对客户端实际收到的 UID、数量与顺序。
4. 资源增速、drop 或长尾持续恶化时停止升档。候选安全容量必须在相同预算下复测，瞬时峰值与长期稳态分别记录。

完整游戏的排队和事务行为由业务 owner 评估；入座成功不代表整局、长期稳态或框架容量达标。游戏与 Table Push 的计时口径、参数和命令统一见[测试模块](../test/README.md#table-push-分段基准)。

## 复测入口

仓库根、本机无外部依赖的基准：

```powershell
go test ./network/internal/inbound -run '^$' -bench '^BenchmarkAuthenticatedDispatcher$' -benchmem -benchtime=1s -count=3
go test ./network/tcp -run '^$' -bench '^BenchmarkTCPSlowConsumerBackpressure$' -benchmem -benchtime=1s -count=3
go test ./network/websocket -run '^$' -bench '^BenchmarkWebSocketServer/request/payload=4000$' -benchmem -benchtime=2s -count=3
go test ./gateway -run '^$' -bench '^BenchmarkBroadcast/sessions=(1000|10000|100000)$' -benchmem -benchtime=1s -count=3
go test ./event/nats -run '^$' -bench '^BenchmarkDispatch$' -benchmem -benchtime=1s -count=3
```

注入专用 `YOLA_REDIS_INTEGRATION`，凭据使用 `YOLA_REDIS_PASSWORD`：

```powershell
go test ./locate/redis -run '^$' -bench '^BenchmarkStatefulForwardRedisLookups$' -benchmem -benchtime=1s -count=3 -cpu=4
```

NATS 容量基准仅支持 Linux。使用专用 `YOLA_NATS_URL`，每格独立进程；small/default/four/close_default 使用64KiB broker，oversized/close_oversized 使用1MiB broker：

```sh
GOMAXPROCS=2 go test ./event/nats -run '^$' -bench '^BenchmarkSubscriptionCapacity$/^default$' -benchtime=1x -count=1 -timeout=60s
```

p99 标明精确分位数或桶上界；B/op 是累计分配，不是常驻内存。文档命令不表示本次已执行或验收通过。
