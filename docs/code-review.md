# 根模块代码审查

本文记录根 Go module 生产代码及包内测试最近一次审查的结论，不完整审查独立 `test/` module、生成文件和示例业务设计；当 Ludo/Whot 调用根模块 API 形成跨模块热路径时，保留与框架性能边界直接相关的证据。这里只记录有代码或 benchmark 证据且尚未关闭的问题；完整数据、部署约束和容量关闭条件分别见[性能基线](./performance.md)与[当前限制](./issues.md)。

## 审查记录（2026-08-24）

原始审查对比基线为 `09995832558c`。以下结论和验证记录属于该轮审查，不代表之后提交或当前工作树已经通过验证；持续边界仍适用，待验证项不构成实施授权。

2026-08-24 以 `8b27f3284c3843a3e6fb30c0ed9a45297407c0eb` 为干净实施基线复核当前实现、直接调用链和相关测试后，没有未关闭的 P0 正确性或安全实现问题，但存在 5 项 P1 性能审查项。本轮进一步收敛了 backend/gateclient 生命周期、Node epoch 清理与回程错误、advertised endpoint、认证期 callback 批量准入及 heartbeat 并发状态，不改变下列性能项的远程往返、队列容量或 benchmark 结论。这里的 P1 表示成本位于请求、连接、fanout 或订阅的线性放大路径，会直接影响延迟、吞吐或容量；“已确认成本”不等于已经确认生产 SLO 违约，待验证项不得在没有真实流量证据时扩大实现范围。

## P1 性能审查项

| ID | 状态 | 影响与证据 | 推荐落点与关闭条件 |
| --- | --- | --- | --- |
| PERF-01 Stateful Locator | 已确认成本 | 已绑定 Forward 在 Gateway 顺序查询 Node binding、Node epoch，Node 收到请求后再次查询 binding 做 fencing；每请求固定 3 次 Redis GET，实 Redis 基线为 1.11～1.69ms，并按 `3 × Stateful QPS` 放大 Redis commands | 保留 Node fencing；先测真实 Redis p99、pool wait 和请求占比，只允许按 `(service, nodeID)` 合并同一时刻的 epoch 查询。不得时间缓存 binding/epoch；若要移除 epoch GET，须先设计 Node 本地 lease deadline 与 fail-closed，再由 Registry 原子发布 `(nodeID, epoch, endpoint)`，见 [I04](./issues.md#性能与验收限制) |
| PERF-02 同步 Push | 高风险待验证 | `node.Server.PushToUID` 每目标同步执行 `LocateGate + Gateway gRPC`；Ludo/Whot table fanout 逐玩家调用，并在完成前占用共享 mailbox worker，四人消息最多串行四组外部 I/O | 先采集 fanout、Locator、gRPC 与 mailbox queue wait；证实 SLO 瓶颈后，先在单个 table job 内按席位数有界并行并等待全部结果，保持相邻 job 顺序。若远程调用量仍是瓶颈，再设计 `PushToUIDs → 按 GateEndpoint 分组 → BatchPush` 及逐 UID 结果语义；不得用增加 mailbox worker 或无界 goroutine 掩盖阻塞，见 [I45](./issues.md#性能与验收限制) |
| PERF-03 WebSocket 与连接内存 | 已优化分配，容量待验证 | 默认 protobuf 的 bounded pooled reader 将 4KB round-trip 从约 29.5KB/29 alloc 降至 19.1KB/20 alloc；immutable prepared frame 将 10 万 Session 的 256B/4KB 广播从约 28.8MB/409.6MB、约 10 万 alloc 降至 352B/4.16KB、2 alloc，no-op fanout 仍为 0 alloc。32 帧 × 10 万连接 × 4KB 仍是逻辑积压上限，但默认 protobuf 广播已按消息共享 Payload；自定义 codec 或独立消息仍可能按连接持有。TCP 固定 buffer 的真实单边 RSS 未确认 | 剩余工作是每连接排队字节观测/预算和慢连接策略；单 Gateway 按 10,000～50,000 五档独立运行，比较每新增 10,000 条连接的 RSS、GC、队列字节、drop、带宽和端到端 p99，50,000 档稳定 30 分钟后关闭当前容量验证，再根据 RSS 判断是否调整 TCP buffer，见 [I41](./issues.md#性能与验收限制) |
| PERF-04 Gate lease 波次 | 高风险待验证 | heartbeat 到期续租没有跨 Session 并发整形；10,000 synthetic delay/timeout 波次观察到约 9,057～10,000 max active，真实 Redis pool、网络和多 Gateway 结果未确认 | 先记录 heartbeat 分布、Redis pool wait、续租 p99、失败与 lease 剩余量；确认同步波次后先加稳定 renewal jitter，10 万连接仍受限时再由 Gateway 生命周期统一持有批量 renewal scheduler。不得直接增加可能令 lease 排队过期的 semaphore 或退避状态机，见 [I34](./issues.md#性能与验收限制) |
| PERF-05 NATS 订阅积压 | 已确认容量成本 | 默认每订阅队列 256、Payload 上限 64KiB，阻塞 handler 时约积压 16MiB；业务 Payload 在出队时校验，不能阻止 broker 已接收的大消息占用本地队列。EventBus dispatch 本身约 6.50ns/0 alloc，不是当前热点 | 生产对齐 broker `max_payload`，按 handler p99、drop、RSS 选择队列容量；若需更早拒绝大消息，应在进入业务队列前校验，不能仅优化 dispatch，见 [I44](./issues.md#性能与验收限制) |

压测基线同样按 P1 对待：tracked Ludo 配置启用 debug console，且部分 `Desc`、JSON 等日志参数在级别过滤前计算。容量验收使用 `info`/`warn`，昂贵参数只在 `slog.Default().Enabled(ctx, slog.LevelDebug)` 为 true 时构造；否则性能数据不能用于关闭上述问题。

## 持续边界

- Gateway 与 Node 分别拥有自己的状态、失败语义和生命周期，不抽取共同生命周期或共享可变状态。
- 服务入口保留原生 Kratos App 和显式装配，不新增 App 类型、配置转交层或隐式 hook；重构必须减少调用方需要理解的规则。
- TCP 与 WebSocket 分别拥有连接、编解码、ticker、I/O 和关闭；复用认证回复规则、heartbeat 原子状态与 callback queue，不引入公共 transport Client/Server 实现层。
- WebSocket 默认 codec 由包内 Google protobuf 实现持有，不接受 Kratos 全局同名注册覆盖；显式 `Codec`/`WithCodec` 始终走 custom fallback。`PreparedConnection` 必须在 `SendPrepared` 返回前完成访问，只能保留 `Marshal` 返回的 immutable bytes。
- 配置校验和协议、并发、资源状态机可以有较高复杂度；告警处理遵守 [AGENTS.md](../AGENTS.md#验证)。
- 性能修改必须减少可观测的远程往返、编码次数、分配或排队字节；只移动 goroutine、扩大 worker/queue 或增加无失效语义的缓存，不视为关闭问题。
- 同一 Gateway/Node 实例仍不支持并发调用 `BeforeStart` 与 `Stop`，也不支持生命周期重试；现有保护只保证误用时的终态和资源安全。
- 并发和生命周期不变量在状态 owner 包做直接测试；TCP/WebSocket 只保留各自协议边界的必要集成路径，不复制 owner 的完整状态矩阵。
- 根模块包内测试参与审查和复杂度统计；独立 `test/` module 单独验证。生成文件处理遵守 [AGENTS.md](../AGENTS.md#绝对红线高风险)。

## 历史验证（2026-08-24）

该轮记录已执行 `make check`、`go test -count=1 ./...`、独立 `test` module 的 `go test -count=1 ./...`、受影响并发包的 `go test -race -count=1`、当时的 `make complexity`，并以该轮实施基线执行显式 Buf breaking 检查及本机 Docker Redis/etcd 的 `TestGatewayNodeIntegration`，均通过；`test/**` 当时没有修改。该轮未运行既有性能 benchmark、外部 NATS 部署验收、10,000～50,000 真实连接阶梯或真实业务 Push profile，因此上表历史性能数据未更新，PERF-02、PERF-04 和生产容量仍标记为「未确认」。

`make complexity` 已在 `2ec4ff6` 移除，当前由 `make lint` 中的 `gocyclo`、`gocognit` 定位复杂度热点。历史命令列表不是必跑清单；当前验证要求见 [AGENTS.md](../AGENTS.md#验证)。
