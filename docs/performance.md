# 性能基线

本文只保留当前热路径成本、最近一次可复核数据和容量验收口径。数据用于选择复测档位和发现回归，不代表生产容量承诺；压测工具的配置与运行方式见 [Ludo README](../test/ludo/README.md#当前-press-入口)。

## 热路径成本

已绑定 Stateful 请求：

```text
Client
  -> Gateway Redis GET Node binding
  -> Gateway Redis GET Node epoch
  -> Gateway unary gRPC Forward
  -> Node Redis GET Node binding
  -> handler
```

请求速率为 `Q` 时，该路径约产生 `3Q` 次 Redis `GET`。Gateway 的 binding 与 epoch 查询存在数据依赖且位于不同 Redis Cluster slot，不能直接 pipeline 或合并为单个 Lua 脚本；Node 查询承担 fencing。未绑定 Stateful 请求只在 Gateway 查询一次 binding，Stateless 请求不访问 Node Locator。

2026-08-17 使用 Redis 8.6.1 Docker（512MiB）、`GOMAXPROCS=4`、`-benchtime=1s -count=3` 复测并取中位数；Windows 使用 Go 1.26.5，VM Linux 使用 Go 1.26.3。每次操作严格执行上述 3 个顺序 GET；串行数据表示单请求查询延迟，并行数据只表示 4 路负载下的吞吐，不是请求延迟。

| Client → Redis | 串行延迟 | 4 路并发 request/s | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Windows → VM | 1.69ms | 2,870 | 1,648 | 24 |
| VM Linux → 同机 Docker | 1.11ms | 3,490 | 1,768 | 33 |

结果确认跨机网络会直接叠加到 3 次顺序查询上，但没有证据支持改变 fencing 或增加缓存一致性状态。生产容量按 `3 × Stateful QPS` 预算，并以真实请求占比、Redis p99 和连接池为验收依据，见 [I04](./issues.md#性能与验收限制)。

Gate lease 默认 TTL 为 60s，仅在 heartbeat 到达且剩余 lease 不超过 30s 时续租，健康状态下续租速率近似 `在线连接数 / 30s`；10 万在线约为每秒 3,333 次续租脚本调用。失败不会触发内部重试循环，但 lease deadline 不前移，后续 heartbeat 会再次尝试，见 [当前限制 I34](./issues.md#性能与验收限制)。

每个 service 复用一个 WRR ClientConn；普通请求按权重选择 SubConn，粘性请求按 Registry instance ID 精确选择同一连接内的 SubConn，不为每个 NodeID 创建独立 ClientConn。Gateway 在请求入口设置 `RPCTimeout`，ClientConn 不再为同一次调用重复创建 timeout context。

Node 请求读取原子发布的 identity 与 lease，不获取 lifecycle mutex。下列历史基线未覆盖新增的本地租约检查与 context 取消关联成本；排空和状态所有权见 [生命周期](./architecture.md#3-生命周期)。

TCP Reader/Writer buffer 均按最大合法帧 `MaxProtoSize + 4B` 创建，即每个 4,100B、每连接固定约 8.0KiB，10 万连接理论约 782MiB；这只计算用户态 I/O buffer，不包含 socket、Session、发送队列和业务状态。WebSocket Upgrader 为每连接保留 4KiB read buffer，write buffer 通过 `WriteBufferPool` 借用，不应按每连接固定 8KiB 预算。真实连接 RSS 仍按 [I41](./issues.md#性能与验收限制) 验收。

## 热路径诊断与优化顺序

2026-08-21 基于 commit `3035fb8` 重新审查请求、Push、广播、heartbeat、mailbox 和 EventBus 路径，并在 macOS/arm64、Apple M1 Pro、Go 1.26.5、`GOMAXPROCS=8` 上做定向 benchmark 与 pprof。当前没有证据表明控制流复杂度是主要性能问题；影响更大的是顺序外部 I/O、同步 Push 占用共享 worker，以及 WebSocket 按连接编码和读包分配。`gocyclo`、`gocognit` 只用于控制流诊断，不能代替这些运行时验证。

连接 read loop 会等待当前 handler 返回后才读取下一帧，Gateway 还在 Session handler 锁内完成 Forward，因此同一连接保持顺序，但慢 Redis、gRPC 或业务 handler 会形成连接内 head-of-line blocking。该顺序同时维护请求次序和关闭交接，不应在没有协议语义与并发测试的情况下改成并行分发。

当前优化优先级为：

1. **压测基线**：tracked Ludo 配置启用 debug console；即使文件日志关闭，console core 仍同步写 stdout，且 `slog.Debug` 的 `Player.Desc`、JSON 和棋盘路径等参数会在级别过滤前求值。容量测试先使用 `info` 或 `warn`，昂贵调试参数只在对应级别启用时构造，否则结果会混入日志 I/O 和无效计算。
2. **Stateful Locator**：先采集 Redis p99、pool wait 和请求占比。按 `(service, nodeID)` 合并同一时刻的 epoch 查询只可能降低并发负载，不保证降低单请求延迟，也不得让首个调用者取消影响其他等待者；删除查询的额外条件见 [I04](./issues.md#性能与验收限制)。Kratos 在 BeforeStart 申请 epoch 前已构建 instance，不能靠运行时修改 metadata map 代替身份准备与发布契约。
3. **Table Push**：[分段基线](#table-push-分段基线) 已复现逐玩家同步 `LocateGate + Gateway gRPC` 占用 mailbox worker、放大等待的现象；[固定到达率对照](#table-worker-固定到达率对照) 支持将 Ludo/Whot worker 默认值改为 `min(tableNum, 16)`。单桌仍串行；百人房间的高频投递应另行评估定向批量定位和 RPC，保留逐 UID 结果、binding 校验和消息顺序。现有广播包含机器人决策与离线状态变更，不能直接并发调用；真实业务 SLO 仍按 [I45](./issues.md#性能与验收限制) 验收。
4. **WebSocket 分配与广播**：默认 codec 已使用 bounded pooled reader 和单次 fanout 共享的 prepared frame，进程内对照见下文。剩余风险是发送队列只按 32 帧限流，尚未观测每连接排队字节、慢连接比例及单边 RSS，按 [I41](./issues.md#性能与验收限制) 验收。
5. **Heartbeat 波次**：同步建连或依赖故障可能让大量续租同时进入 Redis pool，按 [I34](./issues.md#性能与验收限制) 测量后再决定整形方式。
6. **NATS 积压**：dispatch 暂无热点证据，慢 handler 的队列内存与 broker Payload 上限按 [I44](./issues.md#性能与验收限制) 验收。

默认发送队列按帧数限制为 32，不区分 32B 与 4KB 消息。`32 × 100,000 × 4KB ≈ 12.8GB` 是逻辑 Payload 积压上限；默认 protobuf 的同一次广播现已跨连接共享一份 body，不会按连接重复持有这 12.8GB，但自定义 codec、逐连接独立消息以及 frame、channel、Session 和 socket 成本仍然存在。因此真实广播验收必须同时采集排队字节和慢连接比例。Gate client 的 endpoint 解析、全局锁和空闲 timer 存在可消除成本，但当前没有 profile 证据支持其优先于上述路径。

## 最近基线

以下为 2026-08-21 在 macOS/arm64、Apple M1 Pro、Go 1.26.5、commit `3035fb8` 上的单次进程内复测；TCP 使用 `-benchtime=1s`，WebSocket 使用 `-benchtime=2s`。测试不包含真实 Redis、etcd、跨机网络、TLS 和业务 handler，只用于回归比较。

| Transport/方向 | 连接数 | Payload | ns/op | B/op | allocs/op | p99 |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| TCP request | 10,000 | 32B | 12,468 | 388 | 7 | 259.833µs |
| TCP push | 10,000 | 32B | 7,206 | 4 | 1 | 178.792µs |
| TCP request | 10,000 | 4KB | 13,935 | 4,452 | 7 | 334.167µs |
| TCP push | 10,000 | 4KB | 6,836 | 4 | 1 | 196.334µs |
| WebSocket request | 1 | 32B | 33,360 | 1,549 | 13 | 未采集 |
| WebSocket request | 1 | 4KB | 41,080 | 29,472 | 29 | 未采集 |

重复登录在认证流程内同步 best-effort Kick 旧连接，不维护后台队列；其认证延迟会包含一次跨 Gateway RPC，最坏受 `RPCTimeout` 限制。本轮数据没有显示 TCP request/push 存在优先级较高的 CPU 或分配问题；10,000 连接 benchmark 进程同时包含 loopback client 和 server，不能据此推导 Gateway 单边连接 RSS，100,000 真实连接内存仍为「未确认」。WebSocket round-trip 的分配也同时包含 client 和 server，pprof 只用于定位分配来源，不能把 29.5KB/op 全部归因于 Gateway。

2026-08-21 基于父提交 `0c4ab17` 的本次实现使用同机环境、`-benchtime=2s -count=3` 复测：4KB WebSocket request 中位数为 43.24µs、19,132B/20 alloc；分配相对优化前约 29.5KB/29 alloc 分别下降 35% 和 31%，延迟受本机波动影响，不据此声明生产吞吐提升。alloc pprof 中 server `readFrame` 已不再经过 `io.ReadAll`；剩余主要分配来自 benchmark client 的 Gorilla `ReadMessage` 以及 protobuf marshal/unmarshal。

2026-08-20 在 Windows/amd64、Intel i7-9700K、Go 1.26.5、commit `cbce6fa5` 使用 `GOMAXPROCS=8` 测得 Gateway 本地广播扫描和批次调度中位数；连接的 `SendProto` 为 no-op，不包含 protobuf 编码、socket、客户端读取或慢连接，只用于验证 fanout 自身开销和分配回归。1,000/10,000 档使用 `-benchtime=1s -count=3`，100,000 档使用 `-benchtime=3s -count=5`。

| 在线 Session | ns/broadcast | B/op | allocs/op |
| ---: | ---: | ---: | ---: |
| 1,000 | 44,475 | 0 | 0 |
| 10,000 | 333,956 | 0 | 0 |
| 100,000 | 2,601,778 | 0 | 0 |

fanout 在第一次扩容后复用 Session 快照和批次完成 channel，10 万 Session 稳态也没有每条广播堆分配。

2026-08-21 在 commit `3035fb8` 使用 `-benchtime=1x -count=3` 复核：100,000 Session no-op fanout 为 2.23～2.42ms、0 alloc；256B WebSocket 编码为 14.69～16.51ms、约 28.8MB 和 100,001 次分配；4,000B 为 103.17～107.65ms、约 409.6MB 和约 100,000 次分配。该结果是 prepared frame 优化前的对照，确认 session 扫描不是首要成本，按连接重复编码才是广播热点。

基于父提交 `0c4ab17` 的本次实现使用 `-benchtime=2s -count=3` 复测如下；prepared 档仍是 synthetic connection，不包含 channel、socket 和客户端读取，只验证单次 fanout 的 protobuf 编码复用。

| 当前实现 | 在线 Session | Payload | ns/broadcast（中位数） | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: | ---: |
| no-op fanout | 100,000 | - | 2,550,884 | 0 | 0 |
| prepared encoding | 100,000 | 256B | 2,572,956 | 352 | 2 |
| prepared encoding | 100,000 | 4,000B | 2,635,934 | 4,160 | 2 |

prepared frame 消除了随 Session 数量线性增长的 protobuf marshal 与 Payload 分配，同时保持 no-op fanout 的零分配；显式 custom codec 继续逐连接编码，全局同名注册不能进入默认优化路径，避免假定相同 codec 名称具有相同 wire bytes 或输入生命周期。当前数据只关闭进程内重复编码成本，不关闭真实连接的网络、TLS、队列和 RSS 容量问题。

prepared synthetic 档不是真实连接压测；同机运行 client/server 也不能证明 Gateway 单边容量。真实验收使用 5 个独立出口 IP 的 press；50,000 条连接每秒接收一条 256B 消息时，按 protobuf 与帧头粗估出站约 107Mbps，尚未计入 TCP/TLS 开销，具体条件见 [容量验收](#容量验收)。

同日使用 `-benchtime=100000x` 复测 256B NATS 事件；本机档使用测试进程内嵌 NATS，VM 档为 Windows 到 `192.168.152.129` 的 NATS 2.10.29（无 TLS/认证）。固定次数下 B/op 会受 client buffer 扩容摊销影响，只用于同命令回归。

| NATS 路径 | Publish | End-to-End | Dispatch |
| --- | ---: | ---: | ---: |
| 本机 | 787ns，1 alloc | 1.37µs，4 alloc | 18.5ns，0 alloc |
| Windows → VM | 1.18µs，0 alloc | 5.24µs，3 alloc | 未测 |

2026-08-21 在 commit `3035fb8` 对进程内路径复核得到 Publish 321.8ns、206B/1 alloc，End-to-End 546.5ns、670B/4 alloc，Dispatch 6.50ns、0 alloc。当前数据不支持优先优化 EventBus dispatch；风险仍是慢 handler 造成的有界队列积压。

2026-08-20 基于 commit `cbce6fa5`，使用本机外置 NATS 2.10.29 对阻塞 handler 填满订阅队列，每档 `-benchtime=1x -count=3`。下表是中位数；`Payload 积压` 只计业务字节，`Go heap 增长` 还包含排队的 `nats.Msg` 等增量，不包含基线采集前已分配的 channel 本身。

| 队列容量 | Payload | Payload 积压 | Go heap 增长 | 溢出 drop |
| ---: | ---: | ---: | ---: | ---: |
| 1,024 | 256B | 256KiB | 444KiB | 256 |
| 256 | 64KiB | 16MiB | 15.97MiB | 64 |
| 64 | 1MiB | 64MiB | 63.01MiB | 16 |

大 Payload 下 heap 增长基本等于排队 Payload，证实 `capacity × payload` 可作为容量预算。通用默认值据此收紧为队列 256、Payload 64 KiB，按默认上限计算约 16 MiB。`WithMaxPayloadBytes` 在消息出队时才校验，不能阻止其他 NATS 发布者的大消息占用本地队列，因此生产仍必须同步配置 broker `max_payload`。

2026-08-20 在上述 Windows 机器补测 Gateway `Broadcast` admission；使用 256B Payload，并在每轮取走已接纳 Push，只测 Proto 构造、Payload 复制和本地有界队列，不包含 NATS、Session 扫描、编码或 socket。

| Gateway Broadcast 路径 | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: |
| Admission（256B） | 168.1 | 336 | 2 |

以下 Gate lease 故障波次为 2026-08-16 在上述 Windows 机器上的 synthetic Locator 结果；每个连接同时进入续租窗口，`delay` 模拟依赖延迟，`timeout` 使用 100ms RPC deadline。

| 连接数 | 故障模型 | ns/wave | renewals/wave | max active | B/wave |
| ---: | --- | ---: | ---: | ---: | ---: |
| 1,000 | delay 10ms | 15,195,300 | 1,000 | 1,000 | 1,499,896 |
| 10,000 | delay 10ms | 56,325,900 | 10,000 | 10,000 | 14,700,896 |
| 10,000 | timeout 100ms | 111,826,900 | 10,000 | 10,000 | 9,273,744 |

2026-08-21 在 commit `3035fb8` 复核：1,000 个 10ms delay 操作为 11.94～12.59ms、max active 1,000；10,000 个为 24.67～45.12ms、max active 9,057～9,717；10,000 个 100ms timeout 为 118.4～121.3ms、max active 10,000。数值受调度器影响，但同步波次和高并发结论不变。

结果证明一次 heartbeat 只产生一次续租、恢复成功后停止重试，同时也证明 Gateway 不限制跨 Session 的续租并发。该测试不包含真实 Redis pool、网络和多 Gateway 拓扑，只用于复现最坏同步波次，不作为生产容量结论。

## Ludo/Whot 执行边界

- 每张 Table 使用 FIFO mailbox，共享 `min(tableNum, 16)` 个 worker，不提供配置项；单桌串行、跨桌并行。每桌队列 128、batch 64。
- 普通请求使用 `Executor.Call`，队列满立即返回；换桌和恢复使用有独立超时的 `Group.PostAndWait` 等待容量，未开始的任务可取消，已经开始的任务必须完成。
- timer callback 回到目标 mailbox，保持单写者。
- Session Push 默认最多等待 3s；慢 Push 可能占用共享 worker。
- 选桌按少人桌优先做两阶段线性扫描；无可用桌时最多检查 `2 × tableNum`，不维护额外索引。

2026-08-21 在 commit `3035fb8` 对 Ludo 代表实现复核：100/1,000/10,000 张全满桌分别约为 210ns/3.4µs/37.8µs，均为 0 alloc；Whot 使用相同扫描逻辑。当前数据不支持增加选桌索引，同步 Push 的测量与验收见 [I45](./issues.md#性能与验收限制)。

## Table Push 分段基线

2026-09-06 基于父提交 `c9ca9bd` 及本批 `ClientMiddleware`/`BenchmarkTablePush` 实现，在 VMware Linux/amd64、4 vCPU（i7-9700K）、约 7.5GiB RAM、Go 1.26.6、同 VM 独立 Redis 8.6.1 Docker 上测量。`GOMAXPROCS=4`，每档 `-benchtime=3s -count=3`，Ludo/Whot 顺序运行；下表各项分别取三次结果的中位数。

基准调用实际桌广播函数、Redis Locator、Node 和 Gateway gRPC，每桌四名已就座玩家，消息为包含 256B bytes 的 protobuf。下表记录调整前 `2 × GOMAXPROCS` 策略下的 8 worker、每桌队列 128、batch 64，32 个闭环请求按桌轮询；每个请求等待自己的桌任务完成后才提交下一次。计时前完成认证和首次 Push，清空预热指标；日志输出被关闭，OTel 测量成本计入上层耗时。

| 指标 | 计时范围 |
| --- | --- |
| `mailbox_wait` | 提交 `Executor.Call` 到任务开始，包含准入与排队等待 |
| `fanout` | 任务内实际桌广播函数，保留四名玩家的串行 Push |
| `push` | 完整 `node.Server.PushToUID`，包含定位、编码、客户端池获取和 RPC |
| `locate_gate` | Redis Locator 查询及结果处理，包含连接池等待 |
| `gateway_rpc` | Kratos client metrics middleware 内的 Gateway RPC，不包含前面的定位、业务 Payload 编码和客户端池获取 |
| `mailbox_call` | 提交到任务完成并返回的总延迟；Go 输出的 `ns/op` 是并发吞吐折算值，不能当作此延迟 |

所有数值单位为 ms。`p99 上界` 是直方图 p99 所在桶的上界，桶边界从 1µs 按 1.2 倍增长，不能视为精确 p99。

| 游戏 | 桌数 | 每条 Push 注入延迟 | LocateGate 均值 | Gateway RPC 均值 | fanout 均值 | mailbox 等待均值 | mailbox 总延迟 p99 上界 |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Ludo | 8 | 0 | 0.530 | 0.578 | 4.59 | 13.78 | 56.35 |
| Ludo | 8 | 5 | 0.597 | 6.286 | 27.67 | 81.60 | 140.21 |
| Ludo | 64 | 0 | 0.522 | 0.574 | 4.51 | 13.43 | 27.17 |
| Ludo | 64 | 5 | 0.628 | 6.315 | 27.94 | 82.37 | 140.21 |
| Whot | 8 | 0 | 0.515 | 0.581 | 4.51 | 13.50 | 56.35 |
| Whot | 8 | 5 | 0.667 | 6.354 | 28.25 | 83.27 | 140.21 |
| Whot | 64 | 0 | 0.509 | 0.574 | 4.46 | 13.29 | 27.17 |
| Whot | 64 | 5 | 0.656 | 6.345 | 28.16 | 82.99 | 140.21 |

24 个正式样本共完成 79,570 次桌广播、318,280 次 Push，逐样本投递计数一致；Push、LocateGate 和 mailbox 调用错误均为 0。闭环最多 32 个在途任务，未压满每桌 128 的队列，因此这些结果不证明过载时没有 reject。64 桌下，注入延迟使广播均值从约 4.5ms 增至约 28ms，mailbox 等待均值从约 13ms 增至约 83ms；该负载下等待约为执行时间的三倍，与 32 个请求竞争 8 个 worker 相符。

Ludo 64 桌的两档各补采 `-benchtime=5s` CPU/block profile：阻塞栈显示调用方等待 `mailboxCall.wait`，worker 停留在 `PushToUID → gateclient.Push → gRPC Invoke`；CPU 样本主要落在系统调用和 Go runtime。block profile 累加多个 goroutine 的等待，不能将其百分比当作单次请求耗时占比，也不能跨档直接比较累计秒数。

Gateway 以计数接收器替代客户端 socket，5ms 只模拟 RPC 内的处理延迟；Gate lease 设为 1h，本轮没有客户端 heartbeat。测试不包含真实 WebSocket/TCP 收发、跨机/TLS、完整对局、慢客户端发送队列或开放到达率压测。这是闭环饱和负载，8 桌与 64 桌结果接近，不能据此声称 64 桌存在容量拐点；一次迭代只广播一次，也不等于完整玩家操作。I45 的真实对局 SLO 与 I41 的真实连接容量仍待独立验收。

基准运行命令见 [测试模块](../test/README.md#table-push-分段基准)。当前基准跟随 `min(tableNum, 16)` 默认规则，8/64 桌分别使用 8/16 worker，闭环请求并发不变；重新运行不会复现上表 64 桌的旧 8 worker 条件。在仓库根目录可补采代表档位的 profile；Redis 地址和 `GOMAXPROCS` 使用相同环境变量，将 `gateway_delay=5ms` 换成 `gateway_delay=0s` 得到无注入延迟档：

```powershell
go -C test test -o "$env:TEMP/yola-push.test" ./ludo/internal/biz/table -run '^$' -bench '^BenchmarkTablePush/tables=64/gateway_delay=5ms$' -benchtime=5s -count=1 -cpuprofile "$env:TEMP/yola-push-cpu.pprof" -blockprofile "$env:TEMP/yola-push-block.pprof"
go tool pprof -top "$env:TEMP/yola-push.test" "$env:TEMP/yola-push-cpu.pprof"
go tool pprof -top -cum "$env:TEMP/yola-push.test" "$env:TEMP/yola-push-block.pprof"
```

## Table worker 固定到达率对照

2026-09-06 在上述 4 vCPU、Go 1.26.6、独立 Redis 环境，使用 `c9ca9bd` 上的 I45 源码快照补测 1,000 桌、每桌每秒一次操作。每操作在同一个 mailbox 任务内串行广播两次，每次四名接收者，共 8 次 Push；全局每 1ms 发起一次，即 1,000 操作/秒、8,000 Push/秒。发生器按计划时间发起，不等待前一次完成。每样本持续 24 秒；两款游戏各按 8、16、32、32、16、8 的 worker 顺序运行，其他条件不变，无人工延迟注入。

下表区间为每档四个样本的均值范围，完成延迟从提交 mailbox 计至返回；最后一列是最差样本的 p99 所在桶上界，单位均为 ms。

| workers | mailbox 等待均值 | 操作完成均值 | 最差完成 p99 桶上界 |
| ---: | ---: | ---: | ---: |
| 8 | 276.57～832.14 | 284.85～840.70 | 2592.27 |
| 16 | 0.18～3.37 | 8.99～12.15 | 140.21 |
| 32 | 0.02～31.05 | 8.81～43.70 | 602.88 |

12 个样本共完成 288,000 次操作、576,000 次广播、2,304,000 次 Push，逐样本各段调用数与投递数匹配，记录的错误为 0。8 worker 档每个任务平均占用约 8.2～8.5ms，每秒 1,000 次已超出 8 个 worker 的处理预算；发起计划结束后仍需约 0.67～1.46 秒排空。16 明显改善排队，32 未在两款游戏中稳定更好，因此默认固定为 `min(tableNum, 16)`，不新增配置项；队列、batch 和桌内顺序不变。

32 worker 的 Whot 较慢样本同时出现发生器、Redis 和 RPC 延迟上升，原因尚未定位，不能全部归因于 worker 数。各档发生器延迟的最差 p99 桶上界分别为 2.12/2.54/116.84ms，上表完成延迟未包含该部分。16 worker 仍有完成 p99 桶上界 140.21ms 的样本，不能据此承诺尾延迟全部达标。

本轮使用临时固定到达率诊断脚本，与上方闭环 `BenchmarkTablePush` 不同；原始日志、脚本、源码快照和 SHA-256 清单保留在本机诊断产物 `yola-i45-workers-207c2458`。4,000 个 Session 仍为合成接收器，未覆盖真实客户端、完整业务 handler、机器人、心跳、数据库结算、集中到达和长期运行。增加 worker 只提高跨桌并发，百人热点桌的同步投递成本仍需独立验证。

## 容量验收

- I41 的真实广播入口为 `test/gateway`，默认 NATS 为 `nats://127.0.0.1:4222`，可用 `-nats-url` 覆盖；每个 Gateway 实例固定每秒发布 1 个 256B Payload。单 Gateway 验收时，`test/ludo` press 使用 `connect` 场景统计客户端到达量和采样延迟。具体启动参数见 [测试模块](../test/README.md)。
- 固定 5 个独立出口 IP 的 press，分别独立运行 10,000、20,000、30,000、40,000、50,000 五档；每个 press 对应维持 2,000、4,000、6,000、8,000、10,000 条连接。每档重新启动 Gateway/press，保持 commit、配置、press 数量和消息模型不变。
- 使用专用 Redis/etcd，按真实拓扑核对 Gateway 总量/per-IP 上限、Node 座位、Robot 和残留玩家。
- 固定记录 commit、机器、资源限制、日志级别、服务端配置和压测 UID 区间；Ludo 使用 `info`/`warn` 建立容量基线，关闭文件日志不等于关闭 debug console。
- Gateway/Node 采集 CPU、RSS、GC、goroutine、gRPC、mailbox queue wait/reject 和 Push latency。
- Redis/etcd 采集 commands/s、CPU、连接数、延迟和错误；系统采集 socket、丢包、带宽和 FD。
- 每档稳定后排除前 2 分钟预热并采集至少 10 分钟，50,000 档采集 30 分钟；计算每新增 10,000 条连接的资源增量。档位必须达到业务目标和 per-operation SLO，且压测端未先饱和；drop、p99 持续跳升或 CPU/RSS 增速明显变陡时停止升档，上一档至少复测一次后才作为候选安全容量，瞬时峰值只标记为峰值。

## 复现命令

```powershell
go test ./gateway -run '^$' -bench '^BenchmarkGateLeaseRenewalFailureWave$' -benchtime=1x -count=1 -benchmem
go test ./event/nats -run '^$' -bench 'Benchmark(Publish|EndToEnd|Dispatch)$' -benchtime=1s -count=1 -benchmem
go test ./event/nats -run '^$' -bench '^BenchmarkSubscriptionBacklogMemory$' -benchtime=1x -count=3 -benchmem
go test ./gateway -run '^$' -bench '^BenchmarkBroadcastAdmission$' -benchtime=1s -count=1 -benchmem
go test ./gateway -run '^$' -bench '^BenchmarkBroadcast/sessions=(1000|10000|100000)$' -benchtime=1s -count=1 -benchmem
go test ./gateway -run '^$' -bench '^BenchmarkBroadcastWebSocketEncoding/payload=(256|4000)$' -benchtime=1x -count=5 -benchmem
go test ./network/tcp -run '^$' -bench '^BenchmarkTCPServer/connections=(1000|10000)/(request|push)/payload=(32|4000)$' -benchtime=1s -count=1 -benchmem
go test ./network/websocket -run '^$' -bench 'Benchmark(WebSocketServer|Codec)$' -benchtime=2s -count=1 -benchmem
go test -o "$env:TEMP/yola-websocket.test" ./network/websocket -run '^$' -bench '^BenchmarkWebSocketServer/request/payload=4000$' -benchtime=2s -count=1 -cpuprofile "$env:TEMP/yola-ws-cpu.pprof" -memprofile "$env:TEMP/yola-ws-mem.pprof"
go tool pprof -top -sample_index=alloc_space "$env:TEMP/yola-websocket.test" "$env:TEMP/yola-ws-mem.pprof"
go -C test/ludo test ./internal/biz/table -run '^$' -bench '^BenchmarkTryAvailableTablesFull$' -benchtime=1s -count=1 -benchmem
```

真实依赖验证通过环境变量指向专用实例：

```powershell
$env:YOLA_REDIS_INTEGRATION='<dedicated-redis>:6379'
$env:YOLA_ETCD_INTEGRATION='<dedicated-etcd>:2379'
$env:YOLA_NATS_URL='nats://<dedicated-nats>:4222'
go test ./locate/redis -run '^$' -bench '^BenchmarkStatefulForwardRedisLookups$' -benchtime=1s -count=3 -benchmem -cpu=4
go test ./event/nats -run '^$' -bench 'Benchmark(Publish|EndToEnd)$' -benchtime=100000x -count=1 -benchmem
go test ./gateway -run '^TestGatewayNodeIntegration$' -count=1 -v
```

测试结果必须同时记录 commit 和环境；未记录时只能作为临时诊断，不能更新本页基线。
