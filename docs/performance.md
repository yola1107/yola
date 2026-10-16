# 性能验证

本页说明成本模型、验收方法和复测命令。容量结论须对应明确的负载与 SLO；历史测量由 Git 追溯，新基线记录 commit、工具、配置、机器、拓扑和负载。

## 热路径成本

已绑定的 Stateful 请求依次执行 Gateway 查 binding、查 epoch、gRPC Forward、Node 再查 binding。Node 复查用于 fencing，不能为减少 I/O 删除。

正常 GET 速率约为 `3 × 已绑定 Stateful QPS + 未绑定 Stateful QPS`。Stateless 不查 Node Locator；认证、续租和 Push 另计。

Gate lease 默认为 60s，剩余不超过 30s 时由心跳续租。健康状态下，脚本速率约为 `在线连接数 / 30s` 次/秒。Node binding 的 RenewNode 由业务调度，不计入每条请求的固定成本。

| 成本项 | 口径 |
| --- | --- |
| TCP 读写 buffer | 各 4096+4B，每连接约 8KiB，不含 socket、Session、队列和栈 |
| WebSocket buffer | 每连接 4KiB read buffer；write buffer 从 pool 借用 |
| 认证后的业务 FIFO | 每个 HeartbeatHandler 连接一个 worker，默认等待 8 帧，不含当前请求 |
| NATS 积压 | 订阅数 × 队列容量 × broker 实际 payload 上限，加 runtime 开销 |
| 业务同步 Push | LocateGate 与 Gateway RPC 占用调用方 worker；多次 Push 分别计时 |

这些是结构成本，不能换算为实际 RSS 或安全容量。只有当前 benchmark/profile 指出瓶颈后，才决定缓存、队列或并发调整。

## NATS 容量口径

Payload 积压预算为 `订阅数 × 队列容量 × broker max_payload`，另加运行时开销。队列容量为 256 时，单订阅的 payload 预算为：

- broker 上限 64KiB：约 16MiB。
- broker 上限 1MiB：可达 256MiB。

固定并记录 broker 上限、订阅数和队列容量。接纳与丢弃语义见 [EventBus 容量限制](./eventbus.md#容量限制)，基准见[容量测试](../event/nats/capacity_benchmark_test.go)。

RSS 分别采集接收进程、发布进程和 broker；释放引用不保证 RSS 立即下降。drop 区分本地队列拒绝、出队丢弃及未观测到的传输损失。

## 容量验收

1. 固定连接数目标、消息模型、业务 SLO、commit、机器、独立进程拓扑、TLS 和超时预算。使用任务专用依赖，正式计时期间不并行编译或运行其他压测。
2. 从小规模逐档增加负载，先排除压测端、单 IP 上限和网络出口的瓶颈。分别测量登录突发、稳态、慢连接、重连和故障窗口。
3. 排除预热，记录 CPU、RSS、GC、goroutine、FD、队列等待与拒绝、p99，以及 Redis/etcd/NATS 的延迟和错误。核对客户端实际收到的 UID、数量和顺序。
4. 资源增速、drop 或长尾持续恶化时停止升档。候选安全容量须以相同预算复测，分别记录瞬时峰值和长期稳态。

业务评估完整游戏的排队和事务行为。入座成功不能证明整局、长期稳态或框架容量达标。游戏与 Table Push 的计时口径、参数和命令见[测试模块](../test/README.md#table-push-分段基准)。

## 复测入口

以下命令均在仓库根执行。

### 本机基准

无需外部服务：

```powershell
go test ./network/internal/inbound -run '^$' -bench '^BenchmarkAuthenticatedDispatcher$' -benchmem -benchtime=1s -count=3
go test ./network/tcp -run '^$' -bench '^BenchmarkTCPSlowConsumerBackpressure$' -benchmem -benchtime=1s -count=3
go test ./network/websocket -run '^$' -bench '^BenchmarkWebSocketServer/request/payload=4000$' -benchmem -benchtime=2s -count=3
go test ./gateway -run '^$' -bench '^BenchmarkBroadcast/sessions=(1000|10000|100000)$' -benchmem -benchtime=1s -count=3
go test ./event/nats -run '^$' -bench '^BenchmarkDispatch$' -benchmem -benchtime=1s -count=3
```

### Redis 基准

注入专用 `YOLA_REDIS_INTEGRATION`，凭据使用 `YOLA_REDIS_PASSWORD`：

```powershell
go test ./locate/redis -run '^$' -bench '^BenchmarkStatefulForwardRedisLookups$' -benchmem -benchtime=1s -count=3 -cpu=4
```

### NATS 容量基准

仅支持 Linux，使用专用 `YOLA_NATS_URL`，每个场景独立进程：

- small/default/four/close_default：broker 的 max_payload 为 64KiB。
- oversized/close_oversized：broker 的 max_payload 为 1MiB。

```sh
GOMAXPROCS=2 go test ./event/nats -run '^$' -bench '^BenchmarkSubscriptionCapacity$/^default$' -benchtime=1x -count=1 -timeout=60s
```

p99 须标明精确分位数或桶上界；B/op 表示累计分配，不代表常驻内存。报告结果时注明实际执行的命令，不能以文档示例代替验收记录。
