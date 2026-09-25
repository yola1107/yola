# 问题清单与解决方案

本文汇总问题、设计债和部署约束；已关闭问题保留原条目并划线，实施状态、批次和交接信息由 [架构审查与修复进度](./refactor-progress.md) 维护。
当前架构契约以 [architecture.md](./architecture.md)、[eventbus.md](./eventbus.md) 为准，候选解决方案不代表已经实现或已经获准改变契约。

- **审查基线**：2026-09-25，代码提交 `0c8b270`。行号均为该基线的定位提示，实施前须按声明名重新核对。
- **范围**：根 module 的职责、依赖、状态所有权和生命周期；`test` 是接入与验收案例，业务状态仍由业务层拥有。
- **证据**：I47、I49、I50、I51、I52、I53、I54、I55 已分别完成修复或契约闭环及验证；I46 的框架修复已提交，仍待完整业务验收。原故障及验收记录见进度表；存量运行数据只支持其原始配置和场景。
- **优先级**：P0 为生产前必须闭环的部署风险；P1 为正确性、可用性或容量验收重点；P2 为契约清晰度、扩展能力或已接受限制。
- **维护**：保留既有 ID；新增问题补齐影响、证据、方案和关闭条件。关闭后保留原条目并为问题标题划线，追加关闭结果，在进度表同步划线并保留验证证据；不得删除已关闭问题。

## 架构审查发现

- <a id="i47"></a> **~~I47 · P1：Node 就绪核验与 Registry 发布缺少共同屏障~~**
  - **影响**：旧实例完成准备后暂停，epoch 过期且同 ID 新实例已接手；旧实例恢复时可能先发布旧 endpoint，再因核验失败退出，覆盖新实例的发现记录。请求 fencing 仍有效，主要风险是新实例不可达。
  - **证据**：[node/lifecycle.go:34](../node/lifecycle.go#L34) 在 Start 内核验；固定依赖 github.com/go-kratos/kratos/v3@v3.0.0/app.go:113 在调用 Start 前执行 wg.Done()，随后可进入 Register。contrib/registry/etcd/v3@v3.0.0-20260626125723-668db92c2c00/registry.go:110、163 按 service/ID 无条件 Put；版本见 [go.mod](../go.mod)。[epoch_test.go:68](../node/epoch_test.go#L68) 只覆盖直接 Start 拒绝旧 epoch。
  - **解决方案**：明确准备、就绪、发布、注销的所有者及顺序；比较就绪感知的窄 Registry 接入适配与调整启动编排。先保证核验失败者不能进入发布，再设计旧代不能覆盖、注销新代的注册所有权保护；不引入通用 BaseServer。
  - **验证方案**：用可控屏障暂停旧准备者，令新实例完成同 ID 接手与注册后再恢复旧者；经过真实 kratos.App.Run 验证 endpoint、发现记录和错误。补首次续租阻塞、注册失败、停止竞争及失败回收，最后用专用 Redis/etcd 验证依赖实现的行为。
  - **关闭条件与风险**：旧实例不得覆盖或注销新记录，失败资源可回收且原始错误保留。仅等待 ready 不足以证明跨 Redis/etcd 的代次发布安全；涉及 (NodeID, epoch, endpoint) 的方案须与 I04、I48 一起审查。
  - **关闭记录（2026-09-25，随本问题提交）**：真实 Redis/etcd 上的覆盖、误删各复现 3/3 次后完成修复。用户确认条件注册冲突语义；Node 就绪适配与共享 Registry 的空 key 事务、本代 lease 注销已验收。原 test Registry 提升到根模块，删除重复资源包装，复用官方 Discovery/Watch 和 etcd Session；Kratos 固定 v3.0.0。旧记录未回收时同 ID 启动返回冲突，注册 lease 丢失后须重建应用；不宣称跨 Redis/etcd 原子性。命令、回归与剩余边界见 [B1 验证记录](./refactor-progress.md#b1-results)。

- <a id="i48"></a> **I48 · P1：Node binding 写入缺少存储侧代次保护**
  - **影响**：旧进程已开始的 Bind/Unbind 若因暂停或 I/O 延迟跨过 epoch 失效，同 ID 新进程绑定后，旧 Unbind 仍可能删除新绑定，旧 Bind 仍可能覆盖新值。取消 context 不能撤销已发送的写入。
  - **证据**：[node/session.go:53](../node/session.go#L53) 仅在调用前核验身份，Locator 收到的参数没有 epoch；[locate/redis/node.go:19](../locate/redis/node.go#L19) 直接 SET，[node.go:39](../locate/redis/node.go#L39) 解绑只比较 NodeID。[session_test.go:42](../node/session_test.go#L42) 仅覆盖改绑到不同 NodeID；[epoch_test.go:409](../node/epoch_test.go#L409) 仅覆盖调用前已过期。
  - **解决方案**：区分稳定路由目标 NodeID 与修改者的代次/绑定版本，比较条件更新及新代接管方案。先明确旧 binding 在原 ID 重启后的继承规则，再设计 Locator 能力与存储布局；不能直接把 Node binding 生命周期绑定到 Gate 的 BindingToken。
  - **验证方案**：分别阻塞旧 Bind、旧 Unbind，在新代完成绑定后释放旧写；覆盖相同/不同 NodeID、TTL 失效、取消后迟到完成、进程恢复、旧 Gateway 快照及原 ID 重启。替身复现后验证真实存储条件更新与 Redis Cluster slot 约束。
  - **关闭条件与风险**：已失去修改权的操作不能破坏新绑定，并保持经确认的重启、改绑语义。仅给 Unbind 增加 epoch 比较不能阻止旧 Bind；[decode.go:134](../locate/redis/decode.go#L134) 的 UID binding 与 Node epoch 不同 slot，不能直接增加跨 key Lua。属于存储契约设计，实施前确认重大语义变化。
  - **设计调查与复现（2026-09-25，0883c00）**：真实 go-redis socket Write 屏障中，取消旧租约后释放迟到写入：旧 Bind 覆盖新 Node、旧 Unbind 删除同 ID 新代绑定，在 miniredis/专用 Redis 8.6.1 各失败 3/3；不同 ID Unbind 对照各通过 3/3。真实 Redis 下定向 race 仍为两类功能失败，未报告 data race，不记为验收通过。单节点 Cluster 确认现有两 key 的多 key Lua 返回 CROSSSLOT；同 service slot 原型可拒绝旧代写入，但不是完整迁移验收。
  - **候选方案与边界**：原子核验修改者 epoch 后写/删，可以继续保留 NodeID 值格式与原 ID 继承语义；主要代价是 Node key 共用 service slot。保留 UID 分片则需要额外协调，单纯在 binding 值中追加 epoch 不能阻止旧 Bind。具体接口、失败语义、原型与关闭条件见 [设计调查](./node-binding-fencing.md)。按用户要求维持调查范围，未修改 Locator API、格式或布局，I48 不划线。

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

## 功能与语义缺口

- <a id="i36"></a> **I36 · P1：跨 Gateway 重复登录不保证同 UID 强单活**
  - **影响与证据**：新认证覆盖 Gate binding 后同步 best-effort Kick 旧连接；本地不再丢弃 Kick，但远端失败时旧连接在 lease 失效前仍可 Forward。见 [gateway/takeover.go](../gateway/takeover.go)、[forward.go:20](../gateway/forward.go#L20)。
  - **解决方案**：先明确业务是否要求强单活；若要求，在框架边界设计当前 Gate binding 校验及副作用 fencing，不散布到每个业务 handler。保持 Node binding 独立于物理连接的生命周期。
  - **验证方案**：跨 Gateway 同 UID 并发认证、远端 Kick 失败/超时、旧连接继续请求、旧 Disconnect 迟到、lease 过期和新连接正常请求；检查旧操作实际进入业务的边界。
  - **关闭条件与风险**：按确认的单活强度验收。单次入口校验不能撤销已开始的业务操作；不把 I48 的 Node 代次保护等同于 Gate 单活。

- <a id="i46"></a> **I46 · P1：请求预算分散且与非请求生命周期耦合**
  - **影响与证据**：Transport、Gateway、Node 各有限时；Ludo 入座独立 5s，已开始桌任务可跨过外层 deadline。Node YAML 5s 与压测夹具 15s 的结论不可互用。Gateway RPCTimeout 还用于建连、续租和清理；[node/lifecycle.go:192](../node/lifecycle.go#L192) 把 PushTimeout 用于 epoch 回滚和完整启动失败 Stop，调推送参数会改变回收预算。详见 [预算职责](./architecture.md#63-请求预算与超时职责)。
  - **解决方案**：在装配处表达请求预算，内部继承 deadline 并按职责缩短；分别确定 Push、Auth、租约、失败清理与停服预算所有者。比较现有每层上限与“无 deadline 才补默认值”的行为差异，不新增一个统管所有职责的全局超时。
  - **验证方案**：覆盖较短父 deadline、无 deadline、队列中取消、开始/取消竞争、已开始操作、独立失败清理、重连归属及真实 gRPC/外部帧错误传播；单独调整 PushTimeout 不得意外改变经确认的清理预算。与 I50 联合验证心跳，再按同配置复测负载。
  - **关闭条件与风险**：有效预算及所有者可追踪，取消和已开始操作语义明确，同配置业务目标通过。不得简单把所有 background context 换为请求 context；删除现有上限或改变开始后的完成语义须先确认。
  - **框架修复记录（2026-09-25，随本问题提交）**：Gateway 新增各默认 3s 的 ConnectTimeout、LeaseTimeout、CleanupTimeout，Node 新增默认 3s 的 CleanupTimeout；RPCTimeout/PushTimeout 不再控制这些非请求操作。五条 deadline 耦合各复现 3/3，分离后定向 20 轮通过；真实 gRPC 四种最短 deadline、根包 race、make check/lint、扩展 mailbox/入座/重连/清理 race 通过。保留逐层请求上限及已开始操作语义，未改变协议、游戏默认预算或配置标识符。
  - **剩余验收**：完整游戏与同配置负载目标未运行，不能用上述功能回归关闭整项；维持待验证，与 I45/B6 一并验收。框架代码修复已完整复审并独立提交，后续无需重复实施预算分离；命令和边界见 [B3 记录](./refactor-progress.md#b3-results)。

## 运行与部署限制

- <a id="i40"></a> **I40 · P0：开发 NATS 的认证、TLS 与生产要求不同**
  - **影响与证据**：2026-08-20 开发 VM NATS 2.10.29 INFO 显示认证和 TLS 关闭，只适合受信网络内开发；这是历史观测，本轮未重查当前 VM。broker 启用 JetStream 不改变 Yola 仅使用 Core NATS、无持久化与重放的语义。
  - **解决方案**：生产前开启账号认证、mTLS、subject ACL；保持当前在线事件契约。未来若要求可靠事件，单独设计 JetStream 的 ack、重投、存储与恢复，不隐式切换。
  - **验证方案**：确认目标 broker 后核查当前配置，在获准的隔离环境测试合法/非法凭据、证书和 Topic ACL，并覆盖 I51 的激活错误传播。
  - **关闭条件**：生产安全配置与验收齐备；不因 broker 存在 JetStream 就宣称事件可靠。

- <a id="i03"></a> **I03 · P2：Node binding 固定 6h TTL，缺少独立续租与批量管理**
  - **影响与证据**：[locate/redis/node.go:13](../locate/redis/node.go#L13) 固定 6h，BindNode 刷新 TTL；没有 NodeID 反查或批量清理，长业务可能丢失定位。
  - **解决方案**：当前由长业务在有效生命周期内幂等刷新绑定；只有实际需求成立再设计框架续租/清理，并与 I48 的修改权保护一起评估，不能把 TTL 当存活探测。
  - **验证方案**：模拟 TTL 前后定位、刷新、改绑与旧刷新竞争，覆盖原 ID/新 ID 重启及持续时间超过 TTL 的业务。
  - **关闭条件与风险**：部署明确采用刷新约定并完成验收，或新能力完整实现；不能以延长 TTL 代替生命周期设计。

- <a id="i08"></a> **I08 · P2：service 的 sticky 模式不能安全在线切换**
  - **影响与证据**：Gateway 首次使用 service 后固定 sticky，后续变化 fail closed；见 [gateway/backend.go](../gateway/backend.go)、[gateway/resolver.go](../gateway/resolver.go) 及 [粘性路由](./architecture.md#5-stateful-粘性路由)。
  - **解决方案**：当前 Stateful/Stateless 切换重启全部 Gateway；若确需在线切换，设计独立、版本化的 service 路由策略与 binding 迁移协议，不用实例健康 metadata 隐式触发迁移。
  - **验证方案**：混合新旧 metadata、滚动发布、空实例集、旧快照与既有 binding；验证 fail closed 和经确认的迁移/回滚流程。
  - **关闭条件与风险**：部署严格遵守重启约束，或显式在线切换协议完成验收。没有需求时不预建第二套路由配置。

- <a id="i29"></a> **I29 · P2：代理部署没有可信客户端 IP 边界**
  - **影响与证据**：TCP/WebSocket per-IP 限制和 Auth IP 使用 socket peer，经代理可能把多个客户端计为同一 IP；当前不支持 PROXY protocol 或可信代理头。见 [网络实现](../network)、[gateway/auth.go:162](../gateway/auth.go#L162)。
  - **解决方案**：直接部署沿用 peer；代理需求明确后定义可信代理名单、协议和来源验证，禁止直接信任任意 X-Forwarded-For。
  - **验证方案**：直连、可信/不可信代理、伪造头、IPv4/IPv6、每 IP 限流与传给认证器的地址保持一致。
  - **关闭条件与风险**：部署约束或代理接入方案经验证；不得为了压测绕过来源信任与限流边界。

## 性能与验收限制

- <a id="i41"></a> **I41 · P1：真实连接容量与慢连接内存边界未验收**
  - **影响与证据**：reader、prepared 广播已降低进程内分配，但发送队列只按帧数限制；独立消息、frame/channel/socket 与真实 RSS 仍未验收。现有收益仅见 [最近基线](./performance.md#最近基线)。
  - **解决方案**：先补 I55 的排队字节/drop 观测，再根据真实曲线决定字节预算、慢连接处理和 TCP buffer；不无依据放大队列。
  - **验证方案**：按 [容量验收](./performance.md#容量验收)，单 Gateway、5 个独立出口 IP，独立运行 10,000～50,000 五档；固定配置、消息模型和资源，每档排除预热，记录 RSS/CPU/GC/drop/p99 及每新增 10,000 连接的资源增量，确认压测端未先饱和。
  - **关闭条件**：五档资源与业务 SLO 证据完整，退化档位和候选安全容量可复测；10 万真实连接不是关闭条件，进程内零分配不是容量证明。

- <a id="i45"></a> **I45 · P1：同步 Table Push 与业务尾延迟尚未完成验收**
  - **影响与证据**：Ludo/Whot 默认 min(tableNum, 16) 且桌内串行，同步 Push 占用共享 worker。[单 Gateway 对照](./performance.md#ludo-单-gateway-参数对照) 中 16/128/64、32/64/64 各两轮完成 4,000 人入座，Login p99 分别为 2.98～4.47s、2.66～2.75s；不证明稳态或当前 YAML 预算通过。
  - **解决方案**：框架边界修复后再量化排队、LocateGate、RPC 和发送成本；保留每桌顺序、逐 UID 结果及失败反馈时机。是否定向批量定位/投递须有证据，不直接增加 outbox、并发桌内广播或放大队列。
  - **验证方案**：1,000 桌固定到达率、完整对局、客户端失败窗口、百人热点桌、长期 SLO；记录 mailbox wait/reject、Push 分段耗时和客户端到达，使用与 I46 一致的预算。
  - **关闭条件**：完整业务与稳定负载目标通过；突发入座成功不能关闭。此项属于扩展层验收，不作为当前框架审查的主线。

- <a id="i44"></a> **I44 · P1：NATS 排队内存与 broker Payload 上限尚未闭环**
  - **影响与证据**：默认每订阅 256 条、业务 Payload 64 KiB，理论 Payload 积压约 16 MiB；已有外置 NATS 试验显示 Go heap 随排队 Payload 增长。超限接收消息出队时才校验，实际上限受 broker max_payload 影响；见 [EventBus 容量](./eventbus.md#4-nats-生命周期)。
  - **解决方案**：对齐 broker max_payload，补 I55 的运行中观测；仅在容量证据支持时调整 WithQueueCapacity/WithMaxPayloadBytes，不把出队校验当作入队内存限制。
  - **验证方案**：隔离 broker 中阻塞 handler，改变 Payload、突发量与订阅数，包含超过业务上限但 broker 接受的消息；记录 queue/drop、RSS/heap、handler p99 和释放后的资源。
  - **关闭条件**：给出匹配实际 broker 配置的内存、丢弃与延迟边界；仅有 16 MiB 理论计算不算验收。

- <a id="i04"></a> **I04 · P1：Stateful Forward 固定三次顺序 Redis 查询**
  - **影响与证据**：Gateway 查询 Node binding、epoch，Node 再查 binding 做 fencing；本地租约已实现，远程查询仍为三次，容量约 3 × Stateful QPS。见 [热路径成本](./performance.md#热路径成本)。
  - **解决方案**：先测真实请求占比、Redis p99 和 pool wait。只在有收益时比较同一时刻同 Node epoch 查询合并；不得时间缓存 binding/epoch 或删除 Node fencing。移除 epoch GET 还要求业务副作用 fencing 与 (NodeID, epoch, endpoint) 发布契约，关联 I47/I48。
  - **验证方案**：固定配置对比查询数、p99、pool wait；并发合并须覆盖各调用者独立取消、失败恢复。改变 fencing/发布时覆盖网络分区、续租阻塞、同 ID 新旧进程、旧快照、空实例集及 Cluster slot。
  - **关闭条件与风险**：有可复核性能收益且安全契约不弱化，或部署按现有成本完成容量验收。并发合并不保证单请求更快，当前两次依赖查询不能直接 pipeline，跨 slot 也不能合并为单个 Lua。

- <a id="i34"></a> **I34 · P1：Gate lease 续租波次缺少真实故障容量验收**
  - **影响与证据**：一次到期 heartbeat 最多续租一次，失败由后续 heartbeat 重试；没有跨 Session 并发整形，同步波次按连接数线性放大。见 [gateway/auth.go:139](../gateway/auth.go#L139)、[热路径成本](./performance.md#热路径成本)。
  - **解决方案**：先测真实 heartbeat 分布；确认波次后优先评估保留租约安全余量的稳定 renewal jitter 与连接池校准，没有证据时不增加 semaphore 或退避状态机。
  - **验证方案**：同步/分散建连、Redis 延迟和故障恢复，记录 pool wait、续租 p99、超时、lease 剩余量及连接淘汰；与 I50 区分“心跳没有被处理”和“续租已经发出但拥塞”。
  - **关闭条件**：真实分布及故障容量通过，或经测量的整形方案实现并验证；不能只用理想心跳分布估算生产容量。

## 复核后保留的设计边界

- Gateway discovery 连接池与回程 endpoint 连接池的寻址、回收职责不同；Registry 快照与 ready SubConn 集合也不是同一份状态，不机械合并。
- Node 的可服务 identity 与 epochLease 清理凭据撤下时机不同；请求与出站副作用分开排空，使 Drain 期间仍可完成必要操作。
- TCP/WebSocket 的 framing、writer、关闭语义各自独立；共享 auth、heartbeat、request tracker、queue 的具体不变量，不增加通用 transport 管理层。
- Locator 同时启用 Stateful 与 Gate 寻址是现有约束；没有真实 Stateless PushToUID 需求时不预先拆分。Bus.Close 无 deadline 是明示契约，不作为新缺陷；若需要进程总停机预算，再单独设计。
- 首请求 last-write-wins、原 ID 重启复用定位、不保证 actor 唯一性均为当前语义；改为强单活、唯一抢占或自动迁移必须先确定需求和行为变化。

## 实施与验证通则

- 按 [进度表](./refactor-progress.md) 的当前下一步推进；先稳定复现静态风险，再冻结最小方案。问题证据被代码推翻时更新结论，不按旧计划强行修改。
- 根 module 的契约在根包测试中验收，`test` 用于接入和必要端到端回归；不要只依赖游戏测试通过判断框架正确。
- 按 [AGENTS.md 验证要求](../AGENTS.md#验证) 执行格式化、受影响测试、make lint，以及适用的 make check、race、build、breaking；检查外部依赖条件后再运行。
- 真实 Redis/etcd/NATS 使用专用可丢弃资源，先核对目标、容器/端口/数据范围；共享 VM 的既有服务不可作为默认测试目标。每轮记录代码基线、命令、环境、实际结果与未完成项。
