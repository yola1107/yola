# 问题清单与清理边界

2026-09-26 按 `af33b00` 重新核对。当前任务是根 module 的行为等价清理，Gate/Node 继续作为原生 Kratos v3 App 的内嵌组件。架构契约见 [architecture.md](./architecture.md)，本轮状态与验证见 [进度表](./refactor-progress.md)。

只实施有代码依据、收益明确且风险低的删除、合并、局部命名和控制流清理。保持接口、协议、业务逻辑、求值顺序、锁范围、defer、错误身份及外部 I/O 时机；不把历史候选方案或 skill 能力当作新增需求。

## 本轮清理

| 范围 | 收益与等价边界 | 验证入口 |
| --- | --- | --- |
| Node 注册与分发 | `Handler`、`RegisterRawHandler`、`OnDisconnect` 集中到 [register.go](../node/register.go)；[dispatch.go](../node/dispatch.go) 聚焦分发。声明名、注册锁、panic 与 middleware 顺序不变 | 注册、dispatch、command、原生 App 生命周期测试 |
| Gate/Node 错误处理 | [unbindGate](../gateway/auth.go)、[handleBindingError](../node/session.go) 使用 early return；保留短路、错误分类、取消及日志时机 | 认证、清理、绑定失权与排空测试 |
| Gateway 发现 | [resolver](../gateway/resolver.go) 内联仅初始化两个 map 的单次构造 helper，保留身份冲突校验职责 | 快照冲突、空实例与 sticky 模式测试 |
| Node sticky claim | 前置分支已排除空 claim，删除重复的空 NodeID 判断；仍按 NodeID、epoch、存储 binding 顺序 fencing | dispatch 与 binding fencing 测试 |
| 回程连接池 | [cached](../internal/gateclient/client.go) 提前返回未命中；引用计数、timer 操作及锁范围不变 | 复用、取消、回收与关闭测试/race |

命名返回值已核对：Gateway/Node `BeforeStart` 和 Registry `Register` 的 `err` 参与 defer 回滚；`backendState.mode`、`queue.next` 的名称区分多个返回值语义，保留。本轮核对范围内未发现其他可安全删除的死代码或不必要的命名返回值，不为减少行数扩大改动。

## 保留的问题（本轮不实施）

- <a id="i03"></a> **I03 · Node binding 缺少安全的业务保活能力**
  - **事实**：[locate/redis/node.go](../locate/redis/node.go) 固定 6h TTL；Bind 是覆盖写，进程 epoch 有效不等于仍拥有某个玩家。旧业务任务再次 Bind 可能抢回已改绑的定位。
  - **影响**：业务持续超过 TTL 时可能丢失路由。I48 只保护进程修改权，不解决业务 owner 的存续与释放。
  - **状态**：需独立确定业务存续契约。本轮不增加保活 manager、索引、业务代次或公共 API，也不延长 TTL；原 ID 重启继承与 LWW 保持不变。
  - **后续验收边界**：若另行实施，应覆盖超 TTL 活跃业务、改绑与旧保活竞争、同 ID 重启及结果未知窗口。当前不宣称已有安全保活实现，见 [绑定边界](./node-binding-fencing.md#联合契约边界)。

- <a id="i46"></a> **I46 · Redis deadline 与完整业务预算尚未闭环**
  - **已完成**：框架非请求预算分离、原生 App 验收入口、真实 TCP/WS 取消与已开始任务排空；证据见 [历史记录](./refactor-progress.md#b3-results)、[验收入口](./refactor-progress.md#request-budget-fixture)、[生命周期验收](./refactor-progress.md#request-lifecycle-results)。
  - **剩余证据**：历史真实连接探针在 caller 100ms deadline 后约 301ms 返回成功，3/3 失败；启用 go-redis caller deadline 后的网络 timeout 仍需分类。该探针不是本轮重跑结果，见 [诊断记录](./refactor-progress.md#redis-deadline)。
  - **状态**：未修复；client 装配及错误分类会改变运行行为，超出本轮清理范围。不得修改注入 client、覆盖成功结果或把取消解释为存储回滚。
  - **未完成验收**：完整 Ludo/Whot 副作用与重连窗口、目标规模和稳态未闭环；小规模框架测试不能替代业务验收。

## 从 issue 队列移除的候选方向

删除下列候选方案及自动实施排期，保留对应的事实边界供接入者判断。它们不是“修复完成”，也不因存在工具或历史计划而自动恢复。

| 历史 ID / 方向 | 当前边界 | 移除理由 |
| --- | --- | --- |
| <a id="i36"></a>I36 · Gate 强单活 | 跨 Gateway Kick 为 best-effort；已开始业务操作不撤销 | 无新增强单活需求，强化 fencing 会改变业务接纳及副作用语义 |
| <a id="i08"></a>I08 · sticky 在线切换 | service 模式固定；变化 fail closed，切换须重启 Gateway | 在线迁移需要新路由协议和 binding 迁移，不属于等价清理 |
| <a id="i29"></a>I29 · 代理客户端 IP | 限流与认证使用 socket peer，不信任任意转发头 | 未确定代理部署与信任来源，新增协议会扩大安全和配置边界 |
| <a id="i04"></a>I04 · Stateful 查询合并 | 已绑定请求仍执行三次有各自职责的 Redis 查询 | 无当前瓶颈证据；合并、缓存或删除查询可能弱化 fencing |
| <a id="i34"></a>I34 · 续租整形 | 到期 heartbeat 续租，失败由后续 heartbeat 重试 | 未量化波次成本，新增 jitter、限流或退避状态会改变执行时序 |

## 运行与部署限制

这些是环境和负载的验收边界，不排入本轮框架清理队列。

| 历史 ID / 范围 | 保留的限制与证据 |
| --- | --- |
| <a id="i40"></a>I40 · NATS 部署 | 开发 broker 的历史无认证/TLS 观测不是当前生产环境结论。生产目标须独立验收认证、TLS、ACL；Core NATS 保持在线可丢失语义，见 [接入契约](./eventbus.md) |
| <a id="i41"></a>I41 · 真实连接容量 | 进程内分配与 I55 统计不证明真实连接容量、慢连接 RSS 或业务 SLO；未完成的容量验收不能标为通过 |
| <a id="i45"></a>I45 · Table Push 与业务长尾 | 同步 Push、桌内串行及失败反馈时机不变；已有小场景和入座数据不代表完整对局、目标规模或长期稳态通过 |

<a id="性能与验收限制"></a>
性能历史数据、配置、命令和容量口径保留在 [performance.md](./performance.md)，本轮不运行性能优化或容量扩展。I44 的指定配置验收已完成，结果不外推到其他负载。

## 已关闭问题的历史证据

以下十项保留原 ID、划线和关闭记录，便于追溯。条目中的旧代码行号、候选方案和“本批”均属于原验证基线，不是本轮实施指令或新的通过结论；当前接口与行为以架构文档和代码为准。

- <a id="i47"></a> **~~I47 · P1：Node 就绪核验与 Registry 发布缺少共同屏障~~**
  - **影响**：旧实例完成准备后暂停，epoch 过期且同 ID 新实例已接手；旧实例恢复时可能先发布旧 endpoint，再因核验失败退出，覆盖新实例的发现记录。请求 fencing 仍有效，主要风险是新实例不可达。
  - **证据**：[node/lifecycle.go:34](../node/lifecycle.go#L34) 在 Start 内核验；固定依赖 github.com/go-kratos/kratos/v3@v3.0.0/app.go:113 在调用 Start 前执行 wg.Done()，随后可进入 Register。contrib/registry/etcd/v3@v3.0.0-20260626125723-668db92c2c00/registry.go:110、163 按 service/ID 无条件 Put；版本见 [go.mod](../go.mod)。[epoch_test.go:68](../node/epoch_test.go#L68) 只覆盖直接 Start 拒绝旧 epoch。
  - **解决方案**：明确准备、就绪、发布、注销的所有者及顺序；比较就绪感知的窄 Registry 接入适配与调整启动编排。先保证核验失败者不能进入发布，再设计旧代不能覆盖、注销新代的注册所有权保护；不引入通用 BaseServer。
  - **验证方案**：用可控屏障暂停旧准备者，令新实例完成同 ID 接手与注册后再恢复旧者；经过真实 kratos.App.Run 验证 endpoint、发现记录和错误。补首次续租阻塞、注册失败、停止竞争及失败回收，最后用专用 Redis/etcd 验证依赖实现的行为。
  - **关闭条件与风险**：旧实例不得覆盖或注销新记录，失败资源可回收且原始错误保留。仅等待 ready 不足以证明跨 Redis/etcd 的代次发布安全；涉及 (NodeID, epoch, endpoint) 的方案须与 I04、I48 一起审查。
  - **关闭记录（2026-09-25，随本问题提交）**：真实 Redis/etcd 上的覆盖、误删各复现 3/3 次后完成修复。用户确认条件注册冲突语义；Node 就绪适配与共享 Registry 的空 key 事务、本代 lease 注销已验收。原 test Registry 提升到根模块，删除重复资源包装，复用官方 Discovery/Watch 和 etcd Session；Kratos 固定 v3.0.0。旧记录未回收时同 ID 启动返回冲突，注册 lease 丢失后须重建应用；不宣称跨 Redis/etcd 原子性。命令、回归与剩余边界见 [B1 验证记录](./refactor-progress.md#b1-results)。

- <a id="i48"></a> **~~I48 · P1：Node binding 写入缺少存储侧代次保护~~**
  - **影响**：旧进程已开始的 Bind/Unbind 若因暂停或 I/O 延迟跨过 epoch 失效，同 ID 新进程绑定后，旧 Unbind 仍可能删除新绑定，旧 Bind 仍可能覆盖新值。取消 context 不能撤销已发送的写入。
  - **证据**：[node/session.go:53](../node/session.go#L53) 仅在调用前核验身份，Locator 收到的参数没有 epoch；[locate/redis/node.go:19](../locate/redis/node.go#L19) 直接 SET，[node.go:39](../locate/redis/node.go#L39) 解绑只比较 NodeID。[session_test.go:42](../node/session_test.go#L42) 仅覆盖改绑到不同 NodeID；[epoch_test.go:409](../node/epoch_test.go#L409) 仅覆盖调用前已过期。
  - **解决方案**：区分稳定路由目标 NodeID 与修改者的代次/绑定版本，比较条件更新及新代接管方案。先明确旧 binding 在原 ID 重启后的继承规则，再设计 Locator 能力与存储布局；不能直接把 Node binding 生命周期绑定到 Gate 的 BindingToken。
  - **验证方案**：分别阻塞旧 Bind、旧 Unbind，在新代完成绑定后释放旧写；覆盖相同/不同 NodeID、TTL 失效、取消后迟到完成、进程恢复、旧 Gateway 快照及原 ID 重启。替身复现后验证真实存储条件更新与 Redis Cluster slot 约束。
  - **关闭条件与风险**：存储中已失去修改权的操作不能破坏新绑定，并保持经确认的重启、改绑语义。仅给 Unbind 增加 epoch 比较不能阻止旧 Bind；修复前（`0c8b270`）的 UID binding 与 Node epoch 不同 slot，不能直接追加双 key Lua。[当前 key 编码](../locate/redis/decode.go) 已按用户确认的 A 调整；Lua 原子性不代表 Redis 异步复制中的数据不可回退。
  - **设计调查与复现（2026-09-25，0883c00）**：真实 go-redis socket Write 屏障中，取消旧租约后释放迟到写入：旧 Bind 覆盖新 Node、旧 Unbind 删除同 ID 新代绑定，在 miniredis/专用 Redis 8.6.1 各失败 3/3；不同 ID Unbind 对照各通过 3/3。真实 Redis 下定向 race 仍为两类功能失败，未报告 data race，不记为验收通过。单节点 Cluster 确认现有两 key 的多 key Lua 返回 CROSSSLOT；同 service slot 原型可拒绝旧代写入，但不是完整迁移验收。
  - **关闭记录（2026-09-26，本批提交）**：按用户确认的 A 实施 Node binding/epoch 的 service 同 slot Lua，Bind/Unbind 原子核验调用方 epoch；Node 发现确定失权即取消本代已接纳工作并进入既有失败流程。保持 NodeID 值、6h TTL、有效进程 LWW、原 ID 重启继承、业务 Session API 和唯一 Registry；Gate key 不变，不保留旧布局兼容。旧版本在单机两类迟到写各复现 3/3；修复后真单机/Cluster 写入屏障、失权/迟到续租/Drain、Registry、Gateway 回归及 check/lint/适用 race 通过。Migration、CooperativeFailover 各 3 个独立新建集群的 race 样本通过；连续合跑 race 仍有 manual failover timeout，该组合未通过。首次旧夹具恢复触发的 Redis 断言与修正后的超时分别保留，见 [完整证据](./refactor-progress.md#i48-results)。同 service 单 slot、本地取消不撤销已开始 I/O、异步复制可能回退均是边界，不关闭 I03/I36。

- <a id="i49"></a> **~~I49 · P2：Session 绑定副作用未纳入框架排空屏障~~**
  - **影响**：保存 Session 后发起的后台 Bind/Unbind 没有独立在途计数，Stop 是否等待完全取决于业务 Drain。同步 handler 内的调用已经由 requests 保护，不能据此声称所有正常停机都会提前释放 epoch。
  - **证据**：[node/session.go:53](../node/session.go#L53) 的 Bind/Unbind 与 [session.go:83](../node/session.go#L83) 的 Push 准入不同；[node/lifecycle.go:96](../node/lifecycle.go#L96) 只综合 requests、业务 Drain、deliveries 决定释放。[lifecycle_test.go:493](../node/lifecycle_test.go#L493) 覆盖的是仍在 Forward 内的绑定。
  - **解决方案**：优先扩展现有出站副作用屏障，使 Drain 期间所需的 Bind/Unbind/Push 可用，Drain 后关闭准入并等待；相应名称与注释按真实职责收敛。业务仍负责停止自身后台生产者，不增加第三套没有独立职责的 tracker。
  - **验证方案**：保存 Session，阻塞 Locator 写入，检查 Stop 不提前释放 epoch；覆盖 Drain 内操作、关闭准入后拒绝、等待超时、重复 Stop、epoch 失效及同步 handler 原行为，执行受影响包 race。
  - **关闭条件与风险**：框架接纳的绑定副作用均被等待，排空失败不主动释放 epoch，Drain 能力保持。此项只修复本地生命周期覆盖，不能替代 I48 的存储侧保护。
  - **关闭记录（2026-09-25，1a882f6）**：Bind/Unbind 已纳入现有 deliveries 屏障，Drain 期间仍可使用，关闭准入后返回 Unavailable。保存 Session 后的后台写入、等待超时、重复 Stop、epoch 失效及同步 handler 回归通过；排空失败不主动释放 epoch。此项不替代 I48；验证命令见 [B1 验证记录](./refactor-progress.md#b1-results)。

- <a id="i50"></a> **~~I50 · P1：串行业务处理阻塞心跳与连接存活判断~~**
  - **影响**：慢 Forward 或连续请求排队会阻挡心跳读取、回复和 Gate lease 续租。TCP 默认 5s 的心跳检查下，放宽业务预算可能引起误断线；即使每次请求限制为 3s，多条排队也会累计等待。
  - **证据**：[TCP read loop:196](../network/tcp/server_tcp.go#L196)、[WebSocket read loop:96](../network/websocket/server_websocket.go#L96) 同步等待 handler；[gateway/inbound.go:56](../gateway/inbound.go#L56) 串行锁覆盖 Auth/Forward/Heartbeat；[TCP Client:157](../network/tcp/client.go#L157)、[WebSocket Client:141](../network/websocket/client.go#L141) 按心跳状态关闭连接。TCP [reply:121](../network/tcp/channel.go#L121) 等发送容量还使用连接 context。
  - **解决方案**：与 I46 联合比较两种设计：保留串行并限制 pipeline/业务耗时；或保留业务 FIFO 与认证屏障，让认证后控制帧经过独立的有界处理路径。明确读、执行业务、发送、Close/Kick 的交接，不直接删锁或逐请求创建无界 goroutine。
  - **验证方案**：真实 TCP/WebSocket 链路下注入单个慢请求、多个连续请求、发送队列满和慢 socket；检查心跳回复、续租、请求顺序、认证前行为、Kick/Stop、取消及资源增长。覆盖默认预算与放宽预算，运行 race。
  - **关闭条件与风险**：在明确的负载与预算契约内，业务处理不导致错误存活判定，且背压、顺序和停止行为可验证。单纯加大心跳或请求 timeout 不作为关闭证据；新增队列的每连接内存须量化。
  - **实施与验证记录（2026-09-25）**：真实 TCP/WS → Gateway → gRPC 的阻塞 Forward 各复现误断线 3/3。当前实现通过可选 HeartbeatHandler 分离认证后的业务 FIFO 与心跳，普通自定义 handler 保持串行；默认 8 个等待帧，队列满明确关闭连接，TCP 回复在该模式下不等待发送容量。定向 10 轮、默认 TCP 5s/WS 15s 心跳与放宽业务预算、FIFO、认证屏障、过载丢弃、Gate 续租已通过；完整检查与关闭记录见 [B3](./refactor-progress.md#b3-results)。新增内存和限制见 [调度成本](./performance.md#i50-dispatch-cost)，不关闭真实容量或慢存储预算问题。
  - **关闭记录（2026-09-25，随本问题提交）**：按用户逐 issue 提交、最后统一审核的要求完成有界调度；补齐关闭等待续租的屏障/race、worker 取消与待执行丢弃、panic/发送满关闭。最终 make check、make lint、五个受影响包 race 全部通过，两个 module 无 lint 告警。复审已核对认证前拒绝、业务 FIFO、middleware、Close/Kick 与资源归属。新增 worker 的本机空载 heap/stack 已测量；不承诺无界 pipeline、慢 socket 或慢续租依赖下不掉线，I46/I41 继续独立验收。

- <a id="i51"></a> **~~I51 · P1：NATS 连接级 LastError 不能完整代表本次订阅激活结果~~**
  - **影响**：重连后的重复订阅 ACL 错误可能因文本相同被忽略；激活期间 Publish ACL 或 slow-consumer 错误也可能覆盖 SUB 拒绝，导致错误返回成功或错误归属不准确。
  - **证据**：[event/nats/event.go:225](../event/nats/event.go#L225) 在 Flush 前后比较 LastError；[event.go:273](../event/nats/event.go#L273) 按错误文本判断是否陈旧。固定依赖 github.com/nats-io/nats.go@v1.53.1/nats.go:4021、4042 会覆盖同一个连接错误槽。对外承诺见 [NATS 生命周期](./eventbus.md#4-nats-生命周期)。
  - **解决方案**：验证底层可用的订阅级错误能力，或由 adapter 记录完整激活窗口的错误事件并证明归属与完成屏障可靠；替换后删除 LastError 文本猜测。保留真正激活失败后的注册终态保护，不把连接上的所有异步错误归咎于新订阅。
  - **验证方案**：成功订阅后重连并被 ACL 拒绝，再注册同 Topic；交错 SUB ACL、Publish ACL、slow consumer、订阅上限、Flush、Close 和取消。验证既不漏报也不错报；现有 [subscription_test.go:41](../event/nats/subscription_test.go#L41) 的“首次失败后保持终态”不是这些交错的替代。
  - **关闭条件与风险**：每次激活结果符合现有承诺，重复错误和并发错误不会污染归属。若底层不能提供该强度保证，须先明确并确认契约调整，不能仅修改文档掩盖实现缺口。
  - **复现记录（2026-09-25，a888fcb）**：真实内嵌 NATS Server v2.14.5 上，成功订阅后重启同端口为受限 ACL，再注册相同 Topic，漏报 3/3。真实 nats.go 加协议屏障下，SUB ACL → Publish ACL、SUB ACL → slow-consumer、订阅上限 → Publish ACL 三种覆盖均漏报 3/3；其他 Topic 的 SUB ACL 被误归本次调用 3/3。协议端仅用于固定收包顺序，不代替 broker 权限决策验收。
  - **依赖能力**：固定 nats.go v1.53.1 的 `processTransientError` 只为权限错误设置私有 `Subscription.permissionsErr`，并以 Topic/queue 匹配；权限和上限回调的 Subscription 均为 nil。`PermissionErrOnSubscribe(true)` 配合 `NextMsg` 可读到权限错误，但会消费已排队消息且不覆盖订阅上限；`IsValid()` 对被上限拒绝的订阅仍返回 true。阻塞异步回调后，Flush 与 Barrier 仍返回，故不能把两者用作错误回调的完成屏障。能力探针及 race 各 20 轮通过，见 [固定版本源码](https://github.com/nats-io/nats.go/blob/v1.53.1/nats.go) 和 [B2 记录](./refactor-progress.md#b2-results)。
  - **已确认方案**：保留单连接与公共接口，删除 LastError 快照及文本猜测；Subscribe 仅同步确认本地注册和 Flush，ACL/订阅上限作为 NATS 异步错误报告，不归给某次激活；同步激活、Flush、context 失败仍保持注册终态。已向用户说明 ACL/上限拒绝不再保证由 Subscribe 同步返回，用户要求继续后实施；比较见 [I51 方案](./refactor-progress.md#i51-design)。
  - **关闭记录（2026-09-25，随本问题提交）**：删除连接错误猜测，直接将每个异步错误写入 slog，只有底层提供身份才附带 topic。真实重复 ACL、Publish ACL、订阅上限和协议交错均有独立日志；既有订阅及后续有效注册可用。取消、Flush 超时、Close 及同步失败后重连终态回归通过；定向 20 轮、受影响包 race、make check、make lint 通过，两个 module 无 lint 告警。此项按经确认的异步边界关闭，不宣称恢复原同步承诺；部署仍需独立验收 ACL/容量。详见 [B2 验证记录](./refactor-progress.md#b2-results)。

- <a id="i52"></a> **~~I52 · P2：等待注册锁时取消会无谓终止 Bus 后续注册能力~~**
  - **影响**：第二次 Subscribe 在等待前一次 Flush 时被取消，获锁后仍创建底层订阅，再因已取消的 Flush 进入注册终态，使后续有效注册也失败。
  - **证据**：[event/nats/event.go:201](../event/nats/event.go#L201) 在等锁前检查 context，[event.go:217](../event/nats/event.go#L217) 获锁后未重查 caller/activation context 就执行激活；[subscription_test.go:190](../event/nats/subscription_test.go#L190) 只覆盖 Bus Close 导致等待者拒绝。
  - **解决方案**：在持锁且尚未激活前重新检查取消；如需等待期间及时返回，再评估 context-aware 准入。保持进入底层激活后失败则终态的现有规则。
  - **验证方案**：用屏障阻塞第一注册，取消等待中的第二注册，再放行第一注册；要求第一成功、第二返回取消且没有底层订阅副作用、第三次有效注册成功。补 Close 竞争并运行 race。
  - **关闭条件与风险**：激活前取消不污染 Bus，错误身份与真正激活后的失败语义不变。可独立修复，不依赖 I51 完整重构。
  - **关闭记录（2026-09-25，a888fcb）**：原实现的取消污染稳定复现 3/3；获锁后重查调用方 context，取消者不进入底层激活。可控 PONG 屏障与连续 SID 断言确认第一、第三注册成功，第二返回 Canceled 且未发送 SUB/UNSUB。激活后取消仍进入终态，Close 竞争保持 ErrClosed；定向 20 轮、包测试、race、两个 module lint 通过。等待锁期间不保证立即返回；I51 的错误归属另行处理，命令见 [B2 验证记录](./refactor-progress.md#b2-results)。

- <a id="i53"></a> **~~I53 · P2：Node middleware 缺少稳定的业务 command 元数据~~**
  - **影响**：业务 protobuf 已解码，transport operation 仍是内部 /cluster.v1.Node/Forward，标准日志、指标、追踪无法统一区分 command；共享请求类型时不能从类型可靠恢复命令。
  - **证据**：[node/register.go:39](../node/register.go#L39) 向 middleware 传递 typed request；[node/dispatch.go:72](../node/dispatch.go#L72) 只注入 Session。[examples/whot/service.go:21](../examples/whot/service.go#L21) 的 Enter/Leave 共用 Empty 请求，属于实际调用证据。
  - **解决方案**：在注册或 dispatch 边界提供不可变 command 元数据，按需映射业务 operation 并保留底层 RPC 信息；不向业务暴露 ForwardRequest、存储模型或第二套路由表。
  - **验证方案**：两个共享请求类型的 command 经真实 gRPC 调用，middleware 可区分它们；验证 Session、错误身份、调用顺序与 RawHandler 不自动应用 typed middleware 的约定不变。
  - **关闭条件与风险**：调用方可统一使用业务命令信息，不必逐 handler 包装。属于接口增强；是否改变已有 operation 指标标签须先评估调用方和可观测性兼容性。
  - **关闭记录（2026-09-25，随本问题提交）**：增加只读 `node.CommandFromContext`，由 dispatch 按实际 command 注入不可变 context 值；保持现有 Kratos operation，不隐式改变指标标签。共享 Empty 请求的两个 command 经真实 gRPC 验证，在 metadata 注入前缺失 3/3，注入后 20 轮通过；覆盖 Session、middleware 顺序、错误身份、RawHandler 不自动套 typed middleware、0 值及缺失 context。make check、make lint、Node 全包 race 通过，两个 module lint 为 0 issues；完整 diff 与调用链已复审，见 [B5 记录](./refactor-progress.md#b5-results)。

- <a id="i54"></a> **~~I54 · P2：SendProto 的消息所有权没有统一契约~~**
  - **影响**：调用方在 SendProto 返回后复用或修改 Proto/Body，TCP 可能发送修改后的数据或产生竞争，WebSocket 默认路径则已取得编码结果。
  - **证据**：[network/connection.go:23](../network/connection.go#L23) 未说明普通 SendProto 输入所有权；[TCP channel.go:114](../network/tcp/channel.go#L114) 把原指针入队，[WebSocket channel.go:137](../network/websocket/channel.go#L137) 返回前编码；PreparedConnection 已有明确的不可变 bytes 契约。
  - **解决方案**：核对框架、自定义 handler 和 codec 调用方，明确不可变输入/所有权转移规则。若确需返回后立即复用，则比较复制与同步编码；不因接口外观一致就无依据改写编码热路径。
  - **验证方案**：按选定契约覆盖消息共享、Body 别名、广播、custom codec、关闭竞争与 race；若允许返回后复用，增加复用后的内容隔离断言和编码/分配基准。
  - **关闭条件与风险**：两种 transport 的使用约束明确且实际调用方遵守。禁止把修改注释等同于现有竞争已消失；选择复制会产生性能与错误返回时机变化。
  - **调查与选择（2026-09-25）**：串行探针中，SendProto 返回后修改 Cmd，TCP 队列仍观察到修改值，快照断言失败 3/3；WS 已取得编码结果，对照通过 3/3。审计 Gateway Push/Broadcast、transport 回复、客户端 Request 和测试/benchmark 后，未发现仓库内成功发送后继续写入同一消息的生产路径；广播复制 Payload，Prepared.Reset 只替换视图，不修改旧 Proto。选择统一不可变输入契约，保留当前编码位置和错误时机；调用方需要修改时创建独立消息/数据，不增加未经需求支持的热路径复制。
  - **关闭记录（2026-09-25，随本问题提交）**：接口及架构文档统一约束 Proto 和所有可变字段别名，明确 Prepared.Reset 不恢复旧消息写入权；运行实现未改变。真实 TCP/WS × 默认/自定义 codec 的共享只读消息、并发发送、clone 和 Prepared 复用回归 20 轮及 race 20 轮通过；网络三包 race、Gateway 广播 race、最终 make check/lint 通过。关闭的是所有权歧义；TCP 仍不自动隔离违规修改，外部 handler/codec 须遵守该契约。详见 [B5 记录](./refactor-progress.md#b5-results)。

- <a id="i55"></a> **~~I55 · P2：投递链缺少运行中可归属的容量观测~~**
  - **影响**：无法仅凭 Gateway fanout 统计区分 NATS 上游丢失、handler 堆积、广播队列拒绝和连接发送拥塞；I41/I44 的容量验收证据不完整。
  - **修复前证据（346cfaa）**：`event/nats/subscription.go:99` 主要在退订时读取 Dropped，`:136` 的超限计数私有且采样记录日志；[gateway/broadcast.go:21](../gateway/broadcast.go#L21) 只描述本地接纳与 fanout。
  - **解决方案**：由各状态 owner 提供只读统计或观测注入点，覆盖订阅 queue/drop/handler 耗时、连接排队字节和 drop，退出时保留累计值。复用现有 BroadcastStats，避免暴露原生 subscription/channel 或引入统一监控管理层。
  - **验证方案**：分别阻塞 handler、填满广播队列、制造慢连接并改变 Payload 大小；运行中即可识别丢弃层级，计数不重复且并发读取安全；测量新增观测对热路径的成本。
  - **关闭条件与风险**：能为 I41/I44 输出可重复的分层容量证据，仍不能把“入队成功”计为客户端已收到。此项完成不自动关闭容量问题。
  - **关闭记录（2026-09-25，随本问题提交）**：由原订阅和连接 owner 实现可选 SubscriptionStatsProvider/SendStatsProvider，复用 BroadcastStats，未新增 manager、注册表或消息队列。缺少观测能力各复现 3/3；内嵌真实 NATS、慢 writer 屏障、Payload/取消/关闭/并发读及分层拒绝验收完成，定向 race 20 轮、五包 race、最终 make check/lint 通过。关闭后保留本地累计值；原生 drop 明确标记最后可得值，连接逻辑 Payload 不代表 RSS，跨层错误不相加。三组热路径成本已对照，详见 [I55 验证](./refactor-progress.md#i55-results)；本项不关闭 I41/I44。

- <a id="i44"></a> **~~I44 · P1：NATS 排队内存与 broker Payload 上限尚未闭环~~**
  - **影响与证据**：默认每订阅 256 条、业务 Payload 64 KiB，理论 Payload 积压约 16 MiB；已有外置 NATS 试验显示 Go heap 随排队 Payload 增长。超限接收消息出队时才校验，实际上限受 broker max_payload 影响；见 [EventBus 容量](./eventbus.md#4-nats-生命周期)。
  - **解决方案**：对齐 broker max_payload，补 I55 的运行中观测；仅在容量证据支持时调整 WithQueueCapacity/WithMaxPayloadBytes，不把出队校验当作入队内存限制。
  - **验证方案**：隔离 broker 中阻塞 handler，改变 Payload、突发量与订阅数，包含超过业务上限但 broker 接受的消息；记录 queue/drop、RSS/heap、handler p99 和释放后的资源。
  - **关闭条件**：给出匹配实际 broker 配置的内存、丢弃与延迟边界；仅有 16 MiB 理论计算不算验收。
  - **关闭记录（2026-09-26，本批提交）**：修正旧基准仅等待首次 drop 的完成边界，复用 I55 统计实现按接收计数分批充队列；独立发布进程隔离接收 RSS。专用 NATS 2.10.29 的 64 KiB/1 MiB 配置下，6 场景各 3 个独立进程通过，覆盖多订阅、合法/超限积压、排空/直接关闭、heap/RSS 高水位和 handler p99；真实上限协议拒绝、包测试、Linux/Windows 适用 race、两个 module lint 通过。生产队列和默认参数不变；对象可回收不等于 OS 内存立即归还，结果限定当前模型，不替代 I40/I41。数值见 [容量记录](./performance.md#i44-capacity)，失败修正及命令见 [验证记录](./refactor-progress.md#i44-results)。
