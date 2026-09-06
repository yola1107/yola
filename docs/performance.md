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

## 真实 WebSocket 与游戏链路

2026-09-06 基于 `8131705` 和本批测试代码，在上述 VMware Linux/amd64、4 vCPU、约 7.5GiB RAM、Go 1.26.6 环境补测。使用同 VM 独占、可丢弃的 Redis 8.6.1 和 etcd 3.5.21，`GOMAXPROCS=4`；生产代码未变，worker 仍为 `min(tableNum, 16)`、每桌队列 128、batch 64。本节表格中的延迟均为直方图 p99 所在桶的上界，单位 ms。

客户端、Gateway、Node 和观测器在同一进程，经 loopback 真实 WebSocket/gRPC 通信，保留默认 heartbeat；未覆盖跨机或 TLS。CPU 为整个测试进程生命周期的平均用量，100% 表示一个逻辑核；RSS 为该进程峰值，二者包含客户端、服务和观测成本，CPU 还包含建连与排空阶段，均不包含独立 Redis/etcd 进程。正式性能样本顺序执行，未与编译、lint 或其他压测并行。

### 每桌每秒一次操作

`BenchmarkTableCadence` 使用实际桌广播函数和四名真实 WebSocket 接收者，每个任务串行广播两次、共 8 条 Push，Payload 为包含 256B bytes 的 protobuf。桌和玩家为测试夹具，不执行游戏 handler。发生器直接按计划提交 mailbox；逐玩家核对桌号、序号和数量，预热序号不计入结果。`client_scheduled` 从计划提交时刻计至客户端 Push 回调，包含发生器迟到、排队、同步投递和客户端处理等待。

| 游戏 | 桌数 | 时长 | mailbox 等待 p99 上界 | client_scheduled p99 上界 | 任务完成 p99 上界 | CPU % | 峰值 RSS MiB |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Ludo | 100 | 30s | 0.114 | 15.726 | 18.871 | 39.8 | 83.9 |
| Ludo | 500 | 30s | 0.709 | 27.174 | 32.609 | 117.3 | 297.3 |
| Ludo | 1,000 | 30s | 868.147 | 868.147 | 868.147 | 165.0 | 559.7 |
| Whot | 100 | 30s | 0.137 | 18.871 | 18.871 | 40.8 | 83.7 |
| Whot | 500 | 30s | 0.137 | 9.100 | 13.105 | 113.6 | 295.0 |
| Whot | 1,000 | 30s | 32.609 | 46.956 | 56.348 | 159.6 | 561.5 |
| Ludo | 1,000 | 120s | 39.130 | 46.956 | 56.348 | 168.7 | 564.0 |
| Whot | 1,000 | 120s | 27.174 | 39.130 | 46.956 | 168.6 | 568.2 |

任务完成从实际提交 mailbox 计至任务结束；`scheduled_complete` 另计发生器迟到。八个正式样本共完成 336,000 次操作、672,000 次广播、2,688,000 条 Push，投递数量和顺序全部匹配，观测到的 Push、定位和 mailbox 错误均为 0。两款游戏的 1,000 桌长样本各完成 120,000 次操作、960,000 条 Push，耗时均约 120.006s。

Ludo 的 1,000 桌 30s 样本仍有明显尾延迟：mailbox 等待均值 59.704ms，任务执行均值 10.124ms，发生器迟到 p99 上界 10.921ms，LocateGate/RPC p99 上界分别为 1.764/3.048ms。分段结果把主要延迟定位到任务开始前的等待，但尚未定位积压波次的原因。早期与编译重叠的探索样本已排除，868.147ms 在无该干扰的正式样本仍出现；较好的 120s 结果不能覆盖此问题，也不能证明延长运行会消除尾延迟。

因此，本轮证明该拓扑下 1,000 桌、每秒 8,000 条 Push 能在两分钟样本内保持数量与顺序；尚不能承诺 1,000 桌的延迟 SLO。另行通过的 `TestTablePushSocketOrdering` 覆盖每帧延迟 10ms 和旧连接关闭后的同 UID 重连；发送在重连期间暂停，未验证离线重放、并发接管或慢客户端队列溢出。

### 真实游戏操作与启动失败

`TestGameDelivery` 使用真实游戏服务、Redis 玩家仓库及 etcd Registry，保留 Ludo 压测玩家的同步操作、Whot 的异步请求和响应 mailbox。所有玩家收到开局 Push 后计时 30s，按原客户端反馈立即操作，属于闭环负载，不等于每桌每秒一次操作。结束后逐桌查询 Scene，核对人数、UID 归属、座位和在线状态；排空生产者后按 UID 比较 Node Push 与客户端回调的数量及有消息边界的 SHA-256 流摘要。

两款游戏的两桌烟测、一名真人加两名服务器机器人的场景均通过；机器人场景各观察到 7 条实际机器人动作推送。100 桌、400 名真人场景的结果如下：

| 游戏 | 客户端请求往返 p99 上界 | 客户端 Push p99 上界 | 全生命周期匹配 Push 数 | ResultPush 数 | CPU % | 峰值 RSS MiB |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Ludo | 140.211 | 22.645 | 498,840 | 420 | 202.0 | 108.1 |
| Whot | 97.369 | 3.048 | 146,832 | 1,200 | 74.8 | 100.9 |

客户端请求往返从出站帧编码计至响应解码；客户端 Push 从 Node 的 Gateway RPC middleware 入口计至客户端回调，不包含前置定位。Push 和 ResultPush 数覆盖建连、计时与排空的完整生命周期，延迟只采计时阶段，因此不能直接用该 Push 总数除以 30s 计算稳态吞吐。样本观察到了真实结算，但未要求每桌在短样本内完成整局。计时阶段 Ludo/Whot 分别观测到 37,689/10,061 条请求响应；测量边界和客户端早期失败、迟到响应的观测限制见 [测试说明](../test/README.md#固定速率与真实游戏验证)。

500 桌目标规模的两款游戏均在启动阶段失败，尚未达到 2,000 人全部就座开局，因而停止升档，未运行 1,000 桌真实游戏场景：

- Ludo 按每 100ms 一批 20 名玩家、最多 100 个并发启动任务进入，已开局玩家同时持续操作。首个失败为 Login 的 Gateway Forward `DeadlineExceeded`，日志同时出现 `enter player: context deadline exceeded`；重复运行并采集 CPU/block profile 仍复现。默认 Forward deadline 为 3s（`gateway/options.go:64`），入桌路径经过 `Manager.Enter → tryAvailableTables → call → mailbox.Call`（`test/ludo/internal/biz/table/table_mgr.go:108`）。profile 显示调用方等待 mailbox，worker 在 `PushToUID → gateclient.Push → gRPC Invoke` 同步投递中阻塞；累计 goroutine 阻塞时间不能当作单次请求延迟。候选桌竞争、到达波次与 batch 调度各自的贡献尚未分离，现有证据不支持直接增加选桌索引。
- Whot 在逐个建连、异步 Login 与已开局操作重叠时，Scene 请求返回 `ResourceExhausted`，消息为 `node request queue is full`。该错误由 Table mailbox 的 `ErrFull` 映射而来（`test/internal/mailbox/mailbox.go:62`），调用链为 `OnSceneReq → Scene → CallPlayer → mailbox.Call`；不能根据错误文案归因于 Node 全局请求线程池。

下一项诊断应分离登录入桌压力和稳定对局负载，独立采集真实游戏的 mailbox wait/reject、候选桌竞争与同步发送等待，并复现 1,000 桌固定速率的长尾波次。500 桌启动失败不能视为“500 张已开局桌”的容量上限；目前也没有依据放大队列、改用 32 worker 或异步并发桌内广播。百人热点桌、跨机拓扑、长期稳态与失败窗口仍待验证，I45 保持待验证。

运行命令见 [测试模块](../test/README.md#固定速率与真实游戏验证)。原始日志、资源采样、失败 profile、运行脚本、`source-final.tar.gz` 源码快照及 SHA-256 清单保留在本机诊断产物 `yola-i45-ws-20260906-a51d9c2e`；`tested-source.json` 的 317 个 Go 源码与 module 文件已与实际测试的 VM 源码逐项核对。

### Ludo 定桌入座与突发加载对照

2026-09-07 基于 `9d8251d` 加本批 Ludo 定桌入座改动，在同一 4 vCPU VM、Go 1.26.6、独占 Redis/etcd 环境复测。`LoginReq.tableID` 已传递到 `Manager.Enter`，每四个连续 UID 指定同一桌；自动选桌、重连和桌内串行契约见 [Ludo 说明](../test/ludo/README.md#当前游戏流程)。以下只对照 Ludo；仓库默认仍为 16 workers、每桌队列 128、batch 64，未新增配置项。

`TestTableAdmission` 将“入座并完成 Scene/Ready”与“全员收到本局发牌”分开验收。压测启动池容量按本轮玩家总数设置：早期 200 人/秒、启动池 100 的定桌样本中，1,740 次已完成 Login 全部成功，但压测端随后拒绝了启动任务，不能据此判定服务端入座容量。保持全员开局要求的 500 桌样本仍未在等待窗口内通过；其原因尚未确认，不将它记作 500 桌入座上限。

快速对照固定 1,000 桌、4,000 名真人、`GOMAXPROCS=4`，每 10ms 发起 400 人，计划约 100ms 发起完；客户端、Gateway、Node 和观测器同进程，经 loopback WebSocket/gRPC 通信。两组只改变 worker 数，第三组只缩小 batch；所有二进制先编译，正式运行期间不并行检查或其他压测。等待全部 4,000 次启动结束后统一统计，失败不会被重试隐藏。

| workers / 每桌队列 / batch | Login 成功 | Login 失败 | 启动全部结束 | Login p99 | Scene p99 |
| --- | ---: | ---: | ---: | ---: | ---: |
| 16 / 128 / 128 | 2,416 | 1,584 | 3.382s | 2,718.5ms | 309.2ms |
| 32 / 128 / 128 | 2,626 | 1,374 | 3.562s | 2,790.3ms | 370.8ms |
| 32 / 128 / 8 | 2,612 | 1,388 | 3.623s | 2,656.2ms | 2,039.1ms |

三组均完成 4,000 人认证、4,000 次 Login，压测启动池拒绝数为 0；失败发生在 Login，日志包含 `enter player: context deadline exceeded`。成功 Login 的玩家均完成 Scene/Ready。上述阶段 p99 是各阶段最近 1,024 个完成样本（包含失败），不是全体请求的直方图桶上界；计划发起时间不等于连接、认证或入座完成时间。这批历史样本的入桌等待上限为 2s（`test/ludo/internal/biz/handle.go`），外层 Gateway Forward 为 3s。

这一轮 32 workers 比 16 多成功 210 人，但三组都未通过全员入座验收；batch 从 128 缩至 8 没有提高成功数，Scene 尾延迟反而升高，不能依据单轮样本判定最优参数。更早每 100ms 发起 400 人的 16/128/64、32/128/64、64/64/64 三组也均出现入桌超时；那轮首次失败即停止，不与上表比较成功数。定桌消除了自动选桌重试，但突发建连、认证、同步入座推送和共享 worker 等待仍在同一条链路中，现有证据不足以用调参替代进一步定位。

另一次未改变广播路径的 1,000 桌固定速率样本持续 30s，每桌每秒一次操作、每操作 8 条 Push：30,000 次操作及 240,000 条 Push 的数量、顺序均匹配，mailbox 等待与客户端计划时间延迟 p99 桶上界分别为 418.7ms、502.4ms，任务执行均值 9.835ms。该长尾仍未关闭；定桌入座功能不代表稳态延迟问题已经解决。

按后续要求，将实验参数固定为 16 workers、队列 128、batch 32，启动两个 Gateway 实例，各有独立 WebSocket/gRPC 监听和 Redis 客户端，按 UID 奇偶均分玩家。仍为同一测试二进制中的两个实例，不是两个独立进程；客户端与 Node 也在该进程，全部共享 4 vCPU。每个 Gateway 的总连接和单 IP 上限均设为 4,001，本轮各承接 2,000 名玩家。不同加载速度的单轮结果如下：

| 每 100ms 新增人数 | 计划发起完 4,000 人 | Login 成功 | Login 失败 | 启动全部结束 |
| ---: | ---: | ---: | ---: | ---: |
| 200 | 2.0s | 2,419 | 1,581 | 4.302s |
| 400 | 1.0s | 2,516 | 1,484 | 3.883s |
| 600 | 0.7s | 2,276 | 1,724 | 3.462s |
| 800 | 0.5s | 2,269 | 1,731 | 3.462s |
| 1,000 | 0.4s | 2,524 | 1,476 | 3.503s |

五档均完成 4,000 次认证，启动池拒绝数为 0，成功 Login 均完成 Scene/Ready；失败仍为入桌超时，未通过全员入座验收，不能从单轮非单调结果推导限流阈值。两个 Gateway 的 Redis 池各有 40 条连接，生命周期统计存在数千次等待，但池超时计数为 0；累计并发等待时间不能当作单次请求延迟，尚未证明其对入桌超时的主导程度。未观察到连接配额拒绝，现有结果不足以断言 Gateway 限流是根因。早期双 Gateway 共享 Redis 客户端的夹具结果单独归档，不作为此表依据。

命令见 [测试模块](../test/README.md#固定速率与真实游戏验证)。所有样本均先用 `go test -c` 编译，再直接执行二进制，未使用 `go run`。源码快照、参数差异、二进制 SHA-256、原始日志和资源记录保存在本机诊断产物 `yola-ludo-directed-82ad034e`。上述轮次均未通过全员入座，后续超时预算对照见下一节；完整对局和长期 SLO 仍待验证。本轮不修改业务默认 worker、队列容量或 batch。

### Ludo 入桌超时定位与预算对照

继续在同一 4 vCPU VM 上使用两个 Gateway 实例、各自独立 Redis 池、16 workers、队列 128、batch 32，每 100ms 加载 400 人，共 4,000 人。保持同进程 WebSocket/gRPC 拓扑，先编译两个诊断二进制，再顺序运行。诊断仅在临时源码增加入桌排队、Seat 和同步 Push 的耗时记录，未改写广播或调度逻辑；不以这轮数据替代独立进程部署或长期容量验收。

首先只把业务入座等待调到 10s、Gateway RPC 调到 15s，仍有 1,004 次 Login 在约 3s 失败，尽管 4,000 个入桌任务都开始执行。核对发现 WebSocket handler 和 Node handler 仍使用默认 3s；入口 handler context 会向内限制 RPC，仅调 `gateway.RPCTimeout` 不足以放宽完整链路。最终将 WebSocket handler、Gateway RPC、Node handler 都对齐为 15s，客户端请求保持默认 30s，得到以下单轮对照：

| 入桌等待上限 | Login 成功 | Login 失败 | 启动全部结束 | 已开始入桌的排队 p99 | 验收 |
| --- | ---: | ---: | ---: | ---: | --- |
| 2s | 2,455 | 1,545 | 3.589s | 1.970s | 未通过，1,545 次等待截止 |
| 10s | 4,000 | 0 | 4.882s | 3.366s | 通过 1,000 桌座位及逐 UID 消息数量、顺序检查 |

两组启动池拒绝数均为 0，已成功 Login 均完成 Scene/Ready。2s 组有 2,455 个任务开始执行，1,545 次入桌等待截止；`mailboxCall.wait` 只在任务仍未开始时取消并返回 context 错误，已经开始则等待结果。10s 组全部 4,000 个任务开始执行，最大排队 3.407s，未发生入桌等待截止。表中排队 p99 来自所有已开始任务的精确样本；2s 组不包含取消任务，不能拿它与完整样本比较队列性能。

10s 组 Seat 累计执行 64.114s，平均每次 16.029ms；启动快照中同步 Push 平均 3.011ms。`Seat` 会依次向本人和已有玩家交换 UserInfo，再发送 Scene；四名真人入座一桌至少产生 20 次逐 UID Push（不含开局等额外推送），每次同步等待 Redis LocateGate 和 Gateway gRPC。这些 I/O 均占用桌 worker，定桌只能消除自动找桌过程，不能消除共享 worker 等待。快照附近的 24,823 次 LocateGate 和 Gateway RPC 均成功，均值分别约 1.300ms、1.668ms；快照采集时桌任务仍在运行，不能将不同观测点的计数机械相减或把累计并发耗时视作墙钟时间。

该轮 Ludo 首次入座和重连使用独立 10s 等待预算，失败解绑使用新的 2s 清理 context，避免复用已经过期的入桌 context；后续按要求将默认入座等待收紧为 5s。测试 Gateway 的 `-rpc-timeout` 同时传给 WebSocket handler 和 Gateway RPC；本轮夹具的 Node handler 为 15s，当前 Ludo YAML 则为 5s，运行与复现条件见 [测试模块](../test/README.md#本地启动)。框架默认 3s、业务 mailbox 默认 16/128/64 均保持不变。本轮证明放宽等待可以使这一突发样本完成入座，没有证明处理速度提高或稳定对局尾延迟解决；I45 保持待验证。

原始日志、诊断源码差异和二进制哈希归档在本机 `yola-ludo-directed-82ad034e` 的 `timeout-evidence.tar.gz`，核心结果为 `entry2.log`、`entry10.log`；外层 3s 未对齐时的失败日志单独保留。

### Ludo 单 Gateway 参数对照

2026-09-07 将 Ludo 首次入座、重连等待默认值从 10s 收紧为 5s，失败清理保持独立 2s，压测夹具的 WebSocket handler、Gateway Forward、Node handler 仍为 15s，客户端请求保持 30s。夹具直接装配服务，没有读取 Ludo YAML 中的 Node handler 5s；以下结果不覆盖该默认配置。按要求改用单 Gateway，总连接和单 IP 上限均为 4,001，Gateway 使用独立 Redis 客户端。其余保持同一 4 vCPU VM、`GOMAXPROCS=4`、专用 Redis/etcd、每 100ms 加载 400 人，共 4,000 人定向进入 1,000 桌；客户端、Gateway 和 Node 仍在同一测试进程。

两组分别为 A：16 workers / 每桌队列 128 / batch 64，B：32 workers / 每桌队列 64 / batch 64。先预编译两份二进制，再按 A → B → B → A 顺序执行，运行期间没有并行编译、检查或压测。临时诊断源码将客户端阶段采样容量扩为 8,192，并核对每阶段采样数等于完成数，所以下表 p95/p99 使用各轮全部 4,000 次 Login 和已执行入桌任务，不是最近 1,024 次样本，也不是直方图桶上界。

| 执行顺序 | workers / 队列 / batch | Login 成功 / 失败 | 启动完成 | Login p95 | Login p99 | 入桌排队 p95 | 入桌排队 p99 |
| ---: | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| 1 | 16 / 128 / 64 | 4,000 / 0 | 6.343s | 4.385s | 4.472s | 3.711s | 3.858s |
| 2 | 32 / 64 / 64 | 4,000 / 0 | 4.043s | 2.657s | 2.753s | 2.498s | 2.610s |
| 3 | 32 / 64 / 64 | 4,000 / 0 | 4.062s | 2.596s | 2.657s | 2.356s | 2.457s |
| 4 | 16 / 128 / 64 | 4,000 / 0 | 4.342s | 2.901s | 2.984s | 2.772s | 2.861s |

四轮均完成全部认证、Login、Scene/Ready 阶段，启动拒绝和掉线均为 0；未记录入桌队列满或入桌等待截止，1,000 桌座位归属、唯一性及逐 UID 消息数量、顺序检查全部通过。最大入桌排队分别为 3.917s、2.646s、2.497s、2.894s。Scene 请求 p95/p99 分别为 115.5ms/4.076s、223.1ms/1.894s、347.0ms/2.146s、57.3ms/1.989s；它与 Login 分开统计，启动完成取日志 `GAME startup.elapsed`，还包含建连、认证和 Scene/Ready。Ready 阶段在 Scene 已表明玩家准备或游戏中时直接成功，不代表每人都发送了 Ready RPC。

这轮单 Gateway 足以完成目标突发入座。B 组两轮 Login 和排队尾延迟均低于 A 组，但 A 组两轮差异明显，不能据此宣称稳定提升比例或最优 worker 数。两组同时改变 worker 数和队列容量，结果属于组合对照；仓库仍保留默认 16/128/64，32/64/64 只用于临时实验。该验证不要求全员已参与本局，不能替代完整对局或稳态延迟验收。

源码快照、A/B 参数源码、二进制 SHA-256、四轮日志、资源记录及 `results.json` 保存在本机产物 `yola-ludo-groups5s-82ad034e`。原夹具的 `GAME setup workers=16` 是按默认值写出的字段，实验组参数以编译源码和二进制清单为准；上表按实际 A/B 构建记录归组。

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
