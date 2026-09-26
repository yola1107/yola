# 框架优化执行清单

**状态：三轮审查完成，尚未实施。** 基线仍为 `ab0479b`，2026-09-26。保留I56–I64九项P1；第三轮没有新增P0/P1，只补充可选小清理与验收条件，见 [第三轮补充](./architecture-review.md#round3)。完整的问题结构、路径、取舍、风险及估算见 [框架去复杂审查](./architecture-review.md)。本清单不授权 commit、push 或运行环境变更。

优先级只使用 P0（高收益低风险）、P1（收益明确）、P2（收益有限，可选）、Drop（低收益或高风险）。当前没有符合条件的 P0 结构性重构，不为填满优先级制造大改动。

## 待执行候选

| ID / 优先级 | 当前问题与实际位置 | 建议边界 / 收益 | 风险与完成条件 | 状态 |
| --- | --- | --- | --- | --- |
| <a id="i56"></a>I56 / P1 | [queue.go:24](../internal/queue/queue.go#L24) 为两个固定生产调用维护 Option 组合机制 | 内部改为直接构造，删 Option/WithCapacity/WithPanicHandler；预计减24–30生产行、1类型 | 低；保留容量panic、callback终止语义，受影响测试/race及check/lint通过；[完整方案](./architecture-review.md#i56) | 待实施 |
| <a id="i57"></a>I57 / P1 | [tcp/codec.go:118](../network/tcp/codec.go#L118) 的私有frameAppender只有一个实现 | 原默认codec分支直接调用protobuf，删1 interface和2转交方法；预计减10–16生产行 | 低；custom codec fallback、大小校验、Flush/编码/写入顺序不变；[完整方案](./architecture-review.md#i57) | 待实施 |
| <a id="i58"></a>I58 / P1 | [header.go:11](../network/internal/header/header.go#L11) 只有Transport一个生产使用方 | 内收为network私有实现，迁移测试；预计减5–12生产行、1包、1文件 | 低；大小写/多值/空值和conn_id接线保留，不直接换Kratos Metadata；[完整方案](./architecture-review.md#i58) | 待实施 |
| <a id="i59"></a>I59 / P1 | [TCP callback fixture](../network/tcp/client_test.go#L408)、[WS callback fixture](../network/websocket/client_server_test.go#L100) 重复各包基础handler | 复用各自基础fixture，保留可选opened通道；预计减25–35测试行、2类型，不删测试 | 低；保持同步发送、容量、callback/cleanup顺序，原测试及race通过；[完整方案](./architecture-review.md#i59) | 待实施 |
| <a id="i60"></a>I60 / P1 | [NATS接收/panic](../event/nats/subscription_test.go#L243) 与 [统计](../event/nats/stats_test.go#L107) 三场景重复装配 | 合为一个覆盖完整场景；预计减15–25测试行、2顶层测试 | 低；保留独立publisher、Bus.Publish、两次panic后正常返回及全部统计断言，关闭后QueueDroppedCurrent=false也须保留；不能直接删原场景；[完整方案](./architecture-review.md#i60) | 待实施 |
| <a id="i61"></a>I61 / P1 | [prepared.go:18](../network/prepared.go#L18) 同时用CAS发布缓存对象和Once初始化编码 | 保留结果指针，把已有Once上移，删除atomic.Pointer/CAS；预计减8–10生产行、1原子发布机制，不删类型或字段 | 中低；保持可比较性、懒分配、先建state再Marshal、Reset无重叠及旧bytes所有权；需race；[修订方案与被否决方案](./architecture-review.md#i61) | 待实施 |
| <a id="i62"></a>I62 / P1 | [gateway/options_test.go:76](../gateway/options_test.go#L76) 与同文件175行重复依赖缺失测试 | 保留公共NewServer三种缺失case，删重复的resolveOptions测试；预计减30–31测试行、1顶层测试 | 低；精确错误文本与三个输入条件完整保留；[覆盖比较](./architecture-review.md#i62) | 待实施 |
| <a id="i63"></a>I63 / P1 | [test_helpers_test.go:64](../gateway/test_helpers_test.go#L64) 的静态Discovery绕中间对象创建Watcher | Watcher直接持有slice，删watchDiscovery与3个方法/构造函数；预计减14–18测试行、1类型 | 低；Watch时捕获、Next时复制、nil/空语义及context等待不变；[完整方案](./architecture-review.md#i63) | 待实施 |
| <a id="i64"></a>I64 / P1 | [node/options_test.go:80](../node/options_test.go#L80) 与 [server_test.go:24](../node/server_test.go#L24) 重复TLS装配 | 把clone的独特时点断言迁入Node握手测试，再删前者；预计减30–36测试行、1顶层测试 | 低；修改Certificates必须在Option应用后、服务构造前；保留真实握手与关闭等待；[完整方案](./architecture-review.md#i64) | 待实施 |

第二轮独立新增预计净减8–10生产行、74–85测试行；第三轮不提高P1估算。九项累计为47–68生产行、114–145测试行，减少一个内部包、一个生产文件、两个生产类型（含一个私有interface）、三个fixture类型及一套原子发布机制；没有重复计算前次候选。这是静态估算，不是已经取得的收益。P2仅保留在审查报告，未排期；已关闭问题不再重复展开历史解决方案。

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
