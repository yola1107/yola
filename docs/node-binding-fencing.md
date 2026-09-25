# Node binding 修改权设计调查

状态：**I48 已按用户确认的 A 实施并关闭；I03 业务保活继续独立设计**。迟到写入历史证据基于 `0883c00`；本轮从 `70e1777` 实施 service 原子分区，单机与 Cluster 使用相同操作，不为旧布局或 API 建兼容层。[验收与限制](./refactor-progress.md#i48-results) 分别记录成功场景和连续快速管理操作的剩余超时。当前没有容量 SLO。

## 已确认的边界

- NodeID 是稳定路由目标；同 ID 新进程可继承原有定位。binding 由业务显式 Bind/Unbind 维护，TTL 为 6h，不随 Gate 连接关闭或进程 epoch 直接删除。
- 有效 Node 之间保留首请求 last-write-wins；本问题只禁止已经失去进程修改权的写入，不引入 actor 唯一性或跨 Gateway 强单活。
- `node.Server` 持有本代 epoch，`requestSession` 使用时核验本地 identity/lease，已接纳副作用纳入 deliveries 排空。I49 的屏障不能撤回已进入 socket Write 的命令。
- Redis 拥有最终存储修改权。修复前 Session 未把 epoch 传给 Locator，Bind 直接 SET、Unbind 只比较 NodeID；本批 [Session](../node/session.go) 传入本代 epoch，[存储写入](../locate/redis/node.go) 以同 slot Lua 原子核验。不能通过再加一次本地检查或写后检查代替该原子边界。

## 联合契约边界

| 概念 | 当前状态与校验位置 | 不应混同的保证 |
| --- | --- | --- |
| 物理连接归属 | Redis GateBinding 保存当前绑定；Gateway lease 控制本地准入，BindingToken 区分连接事件 | 远端 Kick 是 best-effort，不能由进程 epoch 推导同 UID 强单活 |
| Node 进程修改权 | Node 本地 epochLease 与 Redis epoch key | 本地取消与 Redis 中 epoch 失效不是同一个原子时刻 |
| 玩家业务绑定 | Redis 中的 NodeID，业务决定建立、改绑与释放 | 一个进程仍有效，不代表它仍拥有每个曾经绑定过的玩家 |

- I03 的保活不能复用无条件 Bind：A 的进程仍有效、玩家已经改绑 B 时，A 再 Bind 会覆盖 B；单纯增加 I48 的进程 epoch 核验也会接受这种写入。需先区分显式建立/改绑与“仅当前 owner 延期”，再决定业务生命周期如何驱动保活。当前 Ludo/Whot 仅在首次入座和重连 Bind，尚无覆盖超出 6h 生命周期的安全保活实现。
- 修复前 Gate/Node binding 同用 `service+UID` tag，epoch 用 `service+NodeID`；没有联合原子更新 Gate/Node binding。本批 [key 编码](../locate/redis/decode.go) 让 Node binding/epoch 共用 service tag，Gate 仍按 UID 分布。I36 若要求 Gate token 与 Node 修改联合 fencing，仍需另行设计；当前保持 best-effort，不暗中增强单活。
- 历史红色探针先替换了 Redis epoch，再释放旧写，证明的是“存储中旧代已失权后的迟到破坏”。若本地 lease 先终态失效、Redis 中旧值仍在，Lua 只比较 epoch 值无法得知本地取消。必须定义存储修改权的线性化点，并覆盖迟到续租与结果未知窗口，不能承诺任意取消都撤销外部写入。
- I04 的查询组成和成本测量可独立进行；删除 epoch 查询、把 fencing 与保活合成原语或改变布局时，才依赖本节的共同契约。不能仅为减少查询数弱化修改权或接收端 fencing。

## 迟到写入证据

探针使用真实 go-redis 客户端，在旧连接的 `net.Conn.Write` 入口设可控屏障。本地 Session 校验已完成，随后通过另一连接回收旧 epoch、登记新 epoch 并写入新 binding；取消旧 lease，等调用 context 确认取消后，再释放已开始的 Write。没有在 Locator 中替换为 Background 或移除取消。

| 交错 | miniredis，3 次 | 专用 Redis 8.6.1，3 次 |
| --- | --- | --- |
| 旧 A Bind，B 已成为新绑定 | 3/3 覆盖回 A | 3/3 覆盖回 A |
| 旧 A Unbind，同 ID 新代 A 已绑定 | 3/3 删除新绑定 | 3/3 删除新绑定 |
| 旧 A Unbind，B 已成为新绑定（对照） | 3/3 保留 B | 3/3 保留 B |

这三类中旧调用 context 均已取消，写入均返回 nil，新 epoch 始终保留。说明“取消发生”不等于“已开始的外部写入被撤销”；不代表所有取消调用都会执行成功。探针显式模拟代次交接，未等待自然 30s TTL，也未运行完整 Kratos/Registry 重启或网络分区。

根目录执行 `go test -overlay <overlay.json> ./node -run '^TestAuditI48' -count=3 -timeout=30s -v`；不设 `YOLA_REDIS_INTEGRATION` 时使用 miniredis，设为 `127.0.0.1:16379` 时连接任务专用 Redis DB 9、随机 service。真实 Redis 下同命令加 `-race` 也复现两类功能失败，未报告 data race；**这是红色探针，不是 race 或修复验收通过**。

探针不进入正常测试集。`probe_test.go`、`overlay.json`、`miniredis-red.log`、`redis-red.log`、`redis-race-red.log` 位于本机 `%TEMP%\yola-i48-20260925`；探针 SHA-256 为 `3a61fa7149d4e2cb4c55af5468a835aa599e51dc299a52d9b018a967a110e775`。环境变更后须重建专用实例并重新运行，临时文件不保证长期保留。

## 方案比较

| 方案 | 状态与原子边界 | 代价及结论 |
| --- | --- | --- |
| A：同 service 的 Node epoch 与 Node binding 共用 slot | Node 传本代 epoch；Redis Lua 在同一次操作中比较当前 epoch，再 SET 或按 NodeID 删除。继续只存一份 epoch 和一份 binding | 用户已确认；单机与 Cluster 共用实现，公开 service 单 slot 边界，不替代 I03/I36 |
| B：保留当前 UID 分片并提供跨 slot 协调 | epoch 与 binding 仍由不同 slot 持有，需新增协调或复制修改权，并处理失效/恢复一致性 | 不能用两次 GET/SET 或事务外校验冒充原子性。尚无收益证据支持这套协调成本，不建议提前建设 |
| C：保留 key 布局，仅支持 standalone Redis 的多 key Lua | 单 Redis 节点可直接原子核验两 key | 代码较少但收窄部署契约，Cluster 仍会拒绝；不能作为行为等价的小修，也不以本地 Docker 现状默认采纳 |

只把 binding 值扩为 NodeID/epoch，不在写入时核验当前修改者，仍无法阻止旧 Bind。仅针对历史 I48 探针，A 可以保持 NodeID 字符串并支持原 ID 重启继承；这不证明它已经解决业务 owner 的条件保活或 Gate 强单活，也不预先冻结联合设计所需的值格式。

## A 的具体变更边界（已确认）

1. 保持 `node.Session.BindNode/UnbindNode`、`LocateNode` 以及集群协议不变；框架从自己的 lease 取得本代 epoch，不能由业务传入或伪造。Locator 的 Bind/Unbind 增加 epoch 参数，adapter 只执行约定的存储条件，不持有业务生命周期。
2. Node binding key 为 `locate:node:{base64(service)}:base64(uid)`；epoch key 为 `locate:node:epoch:{base64(service)}:base64(nodeID)`，均使用 RawURL 编码。Gate key 不变；Node binding 仍为 NodeID、TTL 仍为 6h，不新增 UID 索引、缓存、续租 manager 或第二份授权状态。
3. Lua 先比较 epoch key 与调用方 epoch；不存在/不匹配拒绝且不改 binding。匹配时 Bind 保持覆盖写；Unbind 保持“仅当前 NodeID 匹配才删”。Node 侧沿既有 epoch 失效入口处理确定的失权，不能重试为无条件写入。
4. 新部署需一次性切换 Node key 规则；不做自动 fallback 或临时双读双写。即使项目尚未上线，也须先确定旧本地数据处理方式和 Cluster 分片约束，不擅自删除数据。

单节点 Redis Cluster 原型已分配全部 16,384 slots，使用无业务数据的任务专用容器。现有 `game/player` binding 与 `game/node-a` epoch 的 slot 分别为 12689、5067，多 key Lua 返回 `CROSSSLOT`；候选 service tag 的两 key 均为 6294。原型先设置当前 epoch 为 new，再分别以 old 执行 Bind/Unbind：均拒绝且新绑定保留；new 执行匹配 Unbind 成功。这里只验证 Redis 原子原语和 slot 约束，不证明完整 Locator、应用迁移、多节点 Cluster failover 或分区安全已完成。

## 实施后的验收要求

- 先将两类红色探针变成稳定回归；覆盖同/不同 NodeID、旧命令迟到、新代接管、缺失/过期 epoch、正常覆盖和幂等解绑。
- I48 验收进程失权写入、同 ID 重启继承和本地失效早于存储失效的边界；改绑后的迟到保活、无请求但业务仍活跃属于 I03，不能由进程 epoch 推导已完成。
- 核对当前所有 Locator 实现与调用方；测试取消后结果未知、存储拒绝的错误身份、正常 Drain、停止超时，以及请求和业务 Session 契约。
- 用真实 Redis 与多节点 Cluster 验证 Lua、slot、恢复和故障窗口；单节点原型不替代这些验收。
- 按规则运行受影响包测试/race、make check/lint，复审完整 diff、同步文档后独立提交。只有修改权安全与经确认的重启/改绑语义同时成立才能关闭 I48。

## 同时支持单机与 Cluster 的选择依据

2026-09-26，用户再次明确项目尚未上线，不应为旧 key、数据或 API 兼容增加成本，并要求同时支持单机与 Redis Cluster，随后确认“采用 service 分片方案，开始 I48”。I48 的进程修改权与 I03 的业务绑定代次分开交付；前述调查已收敛为 A，后续无需再次请求该方案授权。

推荐 **A：service 作为 Node binding 与进程修改权的共同原子分区，单机与 Cluster 使用同一套 Locator/Lua**。Node keys 的 hash tag 使用 service；Gate binding 不参加 Node epoch 事务，继续按 UID 分布。这是按事务职责选择分片粒度，不是兼容旧布局；不保留旧格式 fallback 或双读双写。C 收窄为单 Redis 的方案不满足新增部署要求，撤回推荐。

该方案允许不同 service 分散到不同 Cluster 节点，但同一 service 的 Node binding/epoch 读写集中在一个 slot。[Redis Cluster 规范](https://redis.io/docs/latest/operate/oss_and_stack/reference/cluster-spec/) 要求多 key 操作处于同一 slot；重分片期间可能暂时不可用，异步复制还存在已确认写丢失窗口。Lua 原子性不代表跨故障切主的数据不可回退。

| I48 变更点 | 具体契约 |
| --- | --- |
| 业务边界 | 本步不要求业务接触 epoch；仅在职责收益明确时调整 API，不为兼容保留中转层 |
| Locator 写入参数 | BindNode/UnbindNode 增加调用者 epoch，由 Node 现有身份提供 |
| Bind | 同一次 Redis 操作先核对当前 epoch，再覆盖 NodeID，仍为 6h TTL；有效进程之间保留既有 LWW |
| Unbind | 同次核对 epoch，仅当前 NodeID 匹配才删除；缺失/其他 NodeID 仍为幂等无操作 |
| 失权与失败 | 缺失/不匹配 epoch 返回可分类错误；框架不新增补偿重试，底层重试由注入 client 配置且每次仍校验 epoch；取消或超时不声明存储已回滚 |
| 启动与部署 | 外层注入单机或 Cluster client，使用相同 Locator；同 slot key 构造由 Redis adapter 统一负责 |
| 原 ID 重启 | binding 仍保留 NodeID，新进程取得新 epoch 后继承路由；旧 epoch 的迟到写拒绝 |
| 布局 | Node keys 一次性切换为 service hash tag；不双读双写，也不自动清理任务外的本地数据 |

若未来明确要求单个 service 按 UID 横向分片，可另选 D：固定 UID 分区、每 Node 每分区独立 authority，与该分区 binding 同 slot，不再保留一份全局 epoch。D 会把每 Node 一次 claim/renew/release 放大为 P 次，需要处理部分成功、结果未知和统一分区映射；它不是简单的 key 调整。任一分区失权可关闭整个 Node 的本地准入，但不能原子撤销其他分区仍有效的存储修改权。当前仅要求两种 Redis 模式，暂不引入这套新授权协调。

| 方案 | 单机 / Cluster | 单 service 的横向分布 | authority 与生命周期成本 |
| --- | --- | --- | --- |
| A（当前推荐） | 同一实现支持 | 单 slot | 每 Node 一份；沿用当前生命周期 |
| D（未选定） | 同一实现支持 | 固定 P 个分区 | 每 Node P 份分区权利；增加部分成功、续租轮次与映射一致性处理 |

I04 的测量应先验证 A 在真实请求组成、Redis 资源和连接池条件下的成本；现有单 UID 三次 GET benchmark 不能证明同 service 多 UID 饱和点，也不能证明 D 的横向收益。

此方案只关闭 I48 的进程失权写入，不关闭 I03。I03 的“当前业务 owner”若要求覆盖同进程 A→B→A 或同 Node 内不同玩家生命周期，必须由同一 binding 记录中的独立业务代次区分；仅增加 NodeID 条件续期无法兑现该要求。业务现有 owner 应保留对应不可变能力，连接重连只更新 Gate Session，不自动更换业务代次。

业务代次的公开 API、Bind 结果未知后的处理以及保活调度仍需在 I03 单独设计：唯一 token 应在 I/O 前可由 owner 持有，条件保活不能抢回改绑，条件释放不能删除新代；已经发送的 LWW Bind 在当前进程仍有效时仍可能迟到，不能把条件清理解释为撤销尚未完成的写入。若连此类有效进程的迟到显式 Bind 也要拒绝，须另行确认从 LWW 改为条件接管，而不是暗中扩大 I48 的承诺。

I48 的 Locator/Node 修改及单 Redis、多主 Cluster 的迟到写、MOVED/ASK、失权和重启回归已完成；连续快速管理操作未全部通过，不作相应稳定性保证。后续先修已复现的 I46 Redis deadline 缺口，再确定 I03 的业务绑定能力与保活契约；I04/I34/I41 按各自证据推进，I44 不重做。
