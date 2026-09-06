# 当前限制

本文只记录未关闭问题与部署约束。`约束` 表示部署必须遵守，`待验证` 表示必须先补运行数据再决定实现，`待设计` 表示需求边界尚未冻结，`待实现` 表示职责和验收已有方案但代码尚未落地；均不代表当前实现或兼容承诺。

## 功能与语义缺口

| ID | 优先级 | 状态 | 影响 | 处理与验收 |
| --- | --- | --- | --- | --- |
| I36 | P1 | 待设计 | 新认证覆盖 Gate binding 后同步 best-effort Kick 旧连接；本地不再丢弃 Kick 任务，但远端失败时旧连接在 lease 失效前仍可 Forward，当前不保证同 UID 强单活 | 明确业务是否要求强单活；若要求，由框架统一校验当前 Gate binding 并补跨 Gateway Kick 失败测试，不把 fencing 分散到每个业务 handler |

## 运行与部署限制

| ID | 优先级 | 状态 | 影响 | 处理与验收 |
| --- | --- | --- | --- | --- |
| I07 | P0 | 约束 | 历史 tracked Redis 口令必须视为已暴露；生产 secret、mTLS/ACL 与入口限流尚需部署闭环 | 轮换所有使用过的凭据（状态「未确认」）；生产验收 secret 管理、传输安全、限流和容量 |
| I40 | P0 | 约束 | 2026-08-20 开发 VM NATS 2.10.29 `INFO` 显示认证和 TLS 关闭，只适合受信网络内开发；broker 已启用 JetStream，但 Yola 只使用 Core NATS API，仍不持久化、不重放 | 生产前开启账号认证、mTLS 和 subject ACL；若未来要求可靠事件，再显式接入 JetStream 并验收存储、副本、ack、重投和故障恢复 |
| I03 | P2 | 约束 | Node binding 默认 6h 后过期，没有独立续租、NodeID 反查或批量清理 | 长业务定期幂等 `BindNode`；不得把 TTL 当存活探测 |
| I08 | P2 | 约束 | Gateway 首次使用 service 后固定 `sticky` 模式，后续模式变化 fail closed | Stateful/Stateless 切换必须重启全部 Gateway；安全在线切换需增加独立、版本化的 service 路由策略和有状态 binding 迁移协议，不能由实例 metadata 直接触发 |
| I29 | P2 | 约束 | TCP/WebSocket 的 per-IP 限制与 Auth IP 使用 socket peer，不支持 PROXY protocol 或可信代理头 | 直接部署；经代理时先设计可信代理边界，禁止直接信任任意 `X-Forwarded-For` |
| I42 | P2 | 约束 | Yola Server 已回收自身初始化失败或 `Stop` 前绑定的内部 listener；Kratos 的后续 `Endpointer` 或 `BeforeStart` hook 失败仍不会自动回滚此前准备的 Server。2026-09-06 临时集成探针确认后续 hook 失败后 Node listener 和 epoch 仍保留 | 启动失败后退出进程，或由 App owner 显式停止自己创建的 Yola Server；不能只调用 `App.Stop` 并假定早期失败已完成清理。若要支持进程继续运行及重建实例，须统一 endpoint、hook、注册阶段的失败回滚，保留 Node 的 Drain/epoch 释放规则，见 [架构复审](./gateway-node-review.md#优先方案一明确应用装配和失败回滚的所有者) |

## 性能与验收限制

| ID | 优先级 | 状态 | 影响 | 验证条件 |
| --- | --- | --- | --- | --- |
| I41 | P1 | 待验证 | 默认 protobuf 已使用 bounded pooled reader，4KB WebSocket round-trip 从约 29.5KB/29 alloc 降至 19.1KB/20 alloc；10 万 Session 的 prepared broadcast 在 256B/4KB 下分别约 352B/2 alloc 和 4.16KB/2 alloc，no-op fanout 保持 0 alloc。默认 codec 不受 Kratos 全局同名注册影响，显式 custom codec 保持独立编码。32 帧 × 10 万连接 × 4KB 仍是逻辑积压上限，但默认 protobuf 广播的同一 Payload 已跨连接共享；独立消息、frame/channel/socket 及真实 RSS 仍未验收 | 单 Gateway 与 5 个独立出口 IP 的 press 分机运行 10,000～50,000 五档，每档排除 2 分钟预热后采集至少 10 分钟，50,000 档采集 30 分钟；固定配置并记录 RSS、广播接受/连接入队 drop、客户端到达量、CPU、GC、带宽、排队帧数与字节数、慢连接比例及端到端 p99，按每新增 10,000 条连接比较增量；根据拐点补充每连接排队字节预算和慢连接策略，并判断是否需要调整 TCP 固定 buffer。10 万真实连接不属于当前关闭条件 |
| I45 | P1 | 待验证 | Ludo/Whot 配置 manager pusher 时，table fanout 会逐玩家同步执行 `LocateGate + Gateway gRPC`，并在完成前占用共享 mailbox worker；四人 fanout 最多串行执行四组外部 I/O，慢 Push 可能同时放大单桌延迟和跨桌 queue wait，真实业务影响尚未 profile | 分段采集 fanout、`LocateGate`、Gateway gRPC、mailbox queue wait/reject 和 Push 错误；若达到实际 SLO 瓶颈，先在单个 table job 内按座位数做有界并行并等待全部完成，验证同玩家消息顺序、离线清理和失败聚合；没有证据时不增加独立 worker pool 或 outbox |
| I44 | P1 | 待验证 | NATS 默认每订阅队列 256、业务 Payload 上限 64 KiB，按默认上限计算的 Payload 积压约 16 MiB；本机外置 NATS 测得大 Payload 的 Go heap 增长接近排队 Payload 字节数。超限消息在出队时才校验，实际上限仍受 broker `max_payload` 影响 | 生产对齐 broker `max_payload`；压测记录队列 drop、RSS 与 handler p99，仅在存在不同容量证据时显式覆盖 `WithQueueCapacity`/`WithMaxPayloadBytes` |
| I04 | P1 | 约束 | 已绑定 Stateful Forward 固定执行 3 次顺序 Redis GET：Gateway 查询 Node binding 与 epoch，Node 再查询 binding 做 fencing | 容量按 `3 × Stateful QPS` 预算，并验证真实请求占比、Redis p99 和连接池；不得时间缓存 UID binding 或删除 Node fencing。若 epoch 重复读取形成实际负载，可评估只合并同一时刻 `(service, nodeID)` 查询的 in-flight coalescing，并验证调用者取消、Node 重启和失败恢复 |
| I34 | P1 | 约束 | 一次到期 heartbeat 最多续租一次；失败后由后续 heartbeat 重试，没有跨 Session 并发整形，同步波次按连接数线性放大 | 生产按真实 heartbeat 分布、Redis pool wait、续租 p99、超时、lease 剩余量和连接淘汰做故障容量验收；确认同步波次后优先评估保留安全余量的稳定 renewal jitter 和连接池校准，没有证据时不增加 semaphore 或退避状态机 |

新增问题必须写清影响、当前证据和关闭条件；问题解决并完成清理验证后从本页删除，由 Git 历史和自动化测试保留结果。
