# 框架优化执行清单

**状态：I56–I64 九项 P1 已实施、验证并复审，随本次清理提交归档。** 2026-09-26，实施基线 `9ca9ad7`；第一轮清理 `ab0479b`、三轮审查 `a9a0cf6` 不重复计收益。[实施记录](./refactor-progress.md#cleanup-results) 保存实际改动、净收益与验证边界；[审查报告](./architecture-review.md) 保留原方案和估算。用户已授权提交全部现有改动，不包含 push 或发布。

优先级只使用 P0（高收益低风险）、P1（收益明确）、P2（收益有限，可选）、Drop（低收益或高风险）。当前没有符合条件的 P0 结构性重构，不为填满优先级制造大改动。

## 本次已完成的 P1

| ID / 优先级 | 当前实现 | 实际净收益 | 保留契约与验收 | 状态 |
| --- | --- | --- | --- | --- |
| <a id="i56"></a>I56 / P1 | [Queue.New](../internal/queue/queue.go#L34) 直接接收容量和 PanicHandler，全部调用方同步 | 生产净减26行、1配置类型、2配置函数 | 非法容量panic、FIFO、普通/terminal callback及关闭协议保留；check/lint与queue、TCP/WS race通过 | 已完成 |
| <a id="i57"></a>I57 / P1 | [TCP codec](../network/tcp/codec.go#L57) 按protoFrameCodec具体类型识别，直接调用protobuf | 生产净减13行、1私有interface、2转交方法 | 保留custom codec fallback、大小校验、Flush/编码/写入顺序及错误身份；check/lint与TCP race通过 | 已完成 |
| <a id="i58"></a>I58 / P1 | [headerCarrier](../network/transport.go#L55) 内收至network；[行为测试](../network/transport_test.go) 迁移，TCP/WS检查conn_id字面量 | 生产净减8行、测试净减1行；包与生产文件各减1 | 大小写、多值、空值及Keys行为原样保留；check/lint及network、TCP/WS race通过 | 已完成 |
| <a id="i59"></a>I59 / P1 | [TCP](../network/tcp/test_helpers_test.go#L13) / [WS](../network/websocket/test_helpers_test.go#L23) 基础fixture接收可选opened通道，替换六处构造 | 测试净减29行、2类型；测试case不减少 | 原通道容量、同步Open发送与cleanup顺序保留；check/lint及TCP/WS race通过 | 已完成 |
| <a id="i60"></a>I60 / P1 | [NATS统计场景](../event/nats/stats_test.go#L108) 合并超限接收与panic恢复覆盖 | 测试净减26行、2顶层测试 | 独立publisher超限且不调用handler，Bus.Publish边界/空payload，两次panic后正常返回；关闭后Calls=3、Panics=2、PayloadDropped=1、QueueDropped=0、Closed=true、QueueDroppedCurrent=false；check/lint及NATS race通过 | 已完成 |
| <a id="i61"></a>I61 / P1 | [PreparedProto](../network/prepared.go#L18) 保留结果指针，Once统一发布，删除atomic.Pointer/CAS | 生产净减7行；新增6行比较/map key编译检查；少1套原子发布机制，类型/字段总数不变 | Once内先建state再Marshal，保持懒编码、nil/error缓存、无重叠Reset及旧bytes；比较检查、check/lint及network、WS、Gateway race通过；panic后的状态由代码顺序复核 | 已完成 |
| <a id="i62"></a>I62 / P1 | [NewServer依赖测试](../gateway/options_test.go#L144) 保留三种缺失条件，删除重复resolveOptions测试 | 测试净减31行、1顶层测试 | 三个输入与精确错误文本均保留，校验前无依赖调用；check/lint与Gateway race通过 | 已完成 |
| <a id="i63"></a>I63 / P1 | [staticDiscovery.Watch](../gateway/test_helpers_test.go#L132) 直接创建持slice的Watcher | 测试净减16行、1类型、3函数/方法 | Watch捕获slice、首次Next复制，nil/空集合、first、后续context等待和Stop语义保留；check/lint与Gateway race通过 | 已完成 |
| <a id="i64"></a>I64 / P1 | [Node TLS握手](../node/server_test.go#L24) 同时验证ServerTLS配置副本，删除独立重复装配 | 测试净减33行、1顶层测试 | 在ServerTLS Option应用后、构造服务前清空原Certificates；保留BeforeStart/Start、health握手及Stop等待；check/lint与Node race通过 | 已完成 |

九项实际净减 **54生产行、130测试行，共184行**，包含迁入测试和新增比较编译检查；少1内部包、1生产文件、2生产类型（含1私有interface）、3个fixture类型及1套原子发布机制。顶层测试470→466，差额来自I60合并2个、I62/I64各1个，header两项测试迁移保留。原静态估算保留在审查报告，不能当作实测性能收益。P2、Drop、I03/I46及业务/容量优化均未纳入；本次九项无未完成项。

## 已知问题与部署限制（不纳入去复杂队列）

| 历史 ID | 当前事实与状态 | 保留依据 |
| --- | --- | --- |
| <a id="i03"></a>I03 | Node binding固定6h TTL，Bind是覆盖写，缺少安全业务保活；未解决。增加业务代次/续租能力不是等价清理 | [node.go:13](../locate/redis/node.go#L13)、[绑定边界](./node-binding-fencing.md#联合契约边界) |
| <a id="i46"></a>I46 | Redis caller deadline与网络timeout分类尚未闭环，完整业务预算仍待验收；未解决。本轮未重现、未修复 | [历史诊断](./refactor-progress.md#redis-deadline)、[预算契约](./architecture.md#63-请求预算与超时职责) |
| <a id="i40"></a>I40 | 生产NATS认证/TLS/ACL须独立验收；历史开发配置不能外推为当前生产结论 | [EventBus接入](./eventbus.md#4-nats-生命周期) |
| <a id="i41"></a>I41 | 真实连接容量、慢连接RSS与业务SLO未完成验收；进程内分配不构成容量证明 | [容量口径](./performance.md#容量验收) |
| <a id="i45"></a>I45 | Table同步Push、完整对局和长期稳态仍有验收边界，不为此修改框架或业务执行模型 | [Table基线](./performance.md#table-push-分段基线) |

<a id="性能与验收限制"></a>
历史成本和容量证据保留在 [performance.md](./performance.md)；本次只评估维护成本，没有性能优化或新的容量结论。

## Drop（不恢复为候选）

| 历史 ID / 方向 | 决定与当前边界 |
| --- | --- |
| <a id="i36"></a>I36 · 强单活 | Drop新增Gate/业务fencing；保留best-effort Kick，不撤销已开始操作 |
| <a id="i08"></a>I08 · sticky在线切换 | Drop在线迁移协议；保留模式固定、变化fail closed、切换重启Gateway |
| <a id="i29"></a>I29 · 代理客户端IP | Drop未定义信任来源的新协议；认证与限流继续使用socket peer |
| <a id="i04"></a>I04 · 查询合并/缓存 | Drop无当前瓶颈证据的机制；保留三次有各自职责的Stateful查询 |
| <a id="i34"></a>I34 · 续租整形 | Drop无测量依据的jitter/退避/限流状态；维持现有heartbeat续租 |

删除生命周期屏障、官方Registrar直接替换、统一TCP/WS运行器、合并两种连接池、删除不同阶段状态等方案均已否决，理由与当前源码见 [审查报告](./architecture-review.md#5-drop-与保持不变的边界)。它们不进入后续实施排期。

## 已完成项（仅历史索引）

第一轮清理已在 `ab0479b` 完成，不重复计入候选收益；[第一轮记录](./refactor-progress.md#第一轮清理记录) 保留验证边界。

| 历史 ID | 已完成内容 | 证据 |
| --- | --- | --- |
| <a id="i47"></a>~~I47~~ | Node就绪与条件登记 | [B1](./refactor-progress.md#b1-results) |
| <a id="i48"></a>~~I48~~ | binding写入的进程代次保护 | [I48](./refactor-progress.md#i48-results)，保留连续Cluster管理操作未通过的限制 |
| <a id="i49"></a>~~I49~~ | 保存Session的绑定副作用排空 | [B1](./refactor-progress.md#b1-results) |
| <a id="i50"></a>~~I50~~ | 认证后业务FIFO与独立心跳 | [B3](./refactor-progress.md#b3-results) |
| <a id="i51"></a>~~I51~~ | NATS激活的异步错误边界 | [B2](./refactor-progress.md#b2-results) |
| <a id="i52"></a>~~I52~~ | 注册等待取消不终止Bus后续注册 | [B2](./refactor-progress.md#b2-results) |
| <a id="i53"></a>~~I53~~ | command元数据 | [B5](./refactor-progress.md#b5-results) |
| <a id="i54"></a>~~I54~~ | 消息不可变所有权契约 | [B5](./refactor-progress.md#b5-results) |
| <a id="i55"></a>~~I55~~ | 投递链本地只读统计 | [I55](./refactor-progress.md#i55-results) |
| <a id="i44"></a>~~I44~~ | 指定broker配置的订阅积压验收 | [I44](./refactor-progress.md#i44-results)，不代表任意负载通过 |

历史记录只支持各自代码和环境，不等于当前候选已验收；旧详细问题和方案可通过Git追溯。
