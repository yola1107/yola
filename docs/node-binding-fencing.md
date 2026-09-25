# Node binding 修改权设计调查

状态：**I48/I03 联合设计，未实施迁移**。迟到写入证据基于 `0883c00`；后续整体审查在 `57a9dfc` 上补充了业务保活和 Gate 归属边界。A/B/C 均为待比较方案，A 不作为已经选定的实施基线。当前用户优先处理组件架构，没有容量 SLO。

## 已确认的边界

- NodeID 是稳定路由目标；同 ID 新进程可继承原有定位。binding 由业务显式 Bind/Unbind 维护，TTL 为 6h，不随 Gate 连接关闭或进程 epoch 直接删除。
- 有效 Node 之间保留首请求 last-write-wins；本问题只禁止已经失去进程修改权的写入，不引入 actor 唯一性或跨 Gateway 强单活。
- `node.Server` 持有本代 epoch，`requestSession` 使用时核验本地 identity/lease，已接纳副作用纳入 deliveries 排空。I49 的屏障不能撤回已进入 socket Write 的命令。
- Redis 拥有最终存储修改权。当前 [Session](../node/session.go#L53) 未把 epoch 传给 Locator；[BindNode](../locate/redis/node.go#L15) 直接 SET，[UnbindNode](../locate/redis/node.go#L39) 只比较 NodeID。不能通过再加一次本地检查或写入后的检查，修复存储中的迟到覆盖。

## 联合契约边界

| 概念 | 当前状态与校验位置 | 不应混同的保证 |
| --- | --- | --- |
| 物理连接归属 | Redis GateBinding 保存当前绑定；Gateway lease 控制本地准入，BindingToken 区分连接事件 | 远端 Kick 是 best-effort，不能由进程 epoch 推导同 UID 强单活 |
| Node 进程修改权 | Node 本地 epochLease 与 Redis epoch key | 本地取消与 Redis 中 epoch 失效不是同一个原子时刻 |
| 玩家业务绑定 | Redis 中的 NodeID，业务决定建立、改绑与释放 | 一个进程仍有效，不代表它仍拥有每个曾经绑定过的玩家 |

- I03 的保活不能复用无条件 Bind：A 的进程仍有效、玩家已经改绑 B 时，A 再 Bind 会覆盖 B；单纯增加 I48 的进程 epoch 核验也会接受这种写入。需先区分显式建立/改绑与“仅当前 owner 延期”，再决定业务生命周期如何驱动保活。当前 Ludo/Whot 仅在首次入座和重连 Bind，尚无覆盖超出 6h 生命周期的安全保活实现。
- 当前 Gate binding 与 Node binding 同用 `service+UID` hash tag，二者同 slot；Node epoch 用 `service+NodeID`。[key 编码](../locate/redis/decode.go#L130) 当前并未联合原子更新 Gate/Node binding；A 保持 Gate key 不变、迁移 Node key，会拆开现有同 slot 关系。如果 I36 要求 Gate token 与绑定修改联合 fencing，布局须重新比较；未确认该需求时继续保持现有 best-effort，不暗中增强单活。
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
| A：同 service 的 Node epoch 与 Node binding 共用 slot | Node 传本代 epoch；Redis Lua 在同一次操作中比较当前 epoch，再 SET 或按 NodeID 删除。继续只存一份 epoch 和一份 binding | 原语简单，但集中 service 热点并拆开 Gate/Node 的 UID 同 slot 关系；需与条件保活和 I36 边界共同评估，尚未选定 |
| B：保留当前 UID 分片并提供跨 slot 协调 | epoch 与 binding 仍由不同 slot 持有，需新增协调或复制修改权，并处理失效/恢复一致性 | 不能用两次 GET/SET 或事务外校验冒充原子性。尚无收益证据支持这套协调成本，不建议提前建设 |
| C：保留 key 布局，仅支持 standalone Redis 的多 key Lua | 单 Redis 节点可直接原子核验两 key | 代码较少但收窄部署契约，Cluster 仍会拒绝；不能作为行为等价的小修，也不以本地 Docker 现状默认采纳 |

只把 binding 值扩为 NodeID/epoch，不在写入时核验当前修改者，仍无法阻止旧 Bind。仅针对历史 I48 探针，A 可以保持 NodeID 字符串并支持原 ID 重启继承；这不证明它已经解决业务 owner 的条件保活或 Gate 强单活，也不预先冻结联合设计所需的值格式。

## A 的具体变更边界（候选，待联合复核）

1. 保持 `node.Session.BindNode/UnbindNode`、`LocateNode` 以及集群协议不变；框架从自己的 lease 取得本代 epoch，不能由业务传入或伪造。Locator 的 Bind/Unbind 增加 epoch 参数，adapter 只执行约定的存储条件，不持有业务生命周期。
2. Node binding key 候选为 `locate:node:{base64(service)}:base64(uid)`；epoch key 为 `locate:node:epoch:{base64(service)}:base64(nodeID)`。Gate key 不变；Node binding 仍为 NodeID、TTL 仍为 6h，不新增 UID 索引、缓存、续租 manager 或第二份授权状态。
3. Lua 先比较 epoch key 与调用方 epoch；不存在/不匹配拒绝且不改 binding。匹配时 Bind 保持覆盖写；Unbind 保持“仅当前 NodeID 匹配才删”。Node 侧沿既有 epoch 失效入口处理确定的失权，不能重试为无条件写入。
4. 新部署需一次性切换 Node key 规则；不做自动 fallback 或临时双读双写。即使项目尚未上线，也须先确定旧本地数据处理方式和 Cluster 分片约束，不擅自删除数据。

单节点 Redis Cluster 原型已分配全部 16,384 slots，使用无业务数据的任务专用容器。现有 `game/player` binding 与 `game/node-a` epoch 的 slot 分别为 12689、5067，多 key Lua 返回 `CROSSSLOT`；候选 service tag 的两 key 均为 6294。原型先设置当前 epoch 为 new，再分别以 old 执行 Bind/Unbind：均拒绝且新绑定保留；new 执行匹配 Unbind 成功。这里只验证 Redis 原子原语和 slot 约束，不证明完整 Locator、应用迁移、多节点 Cluster failover 或分区安全已完成。

## 实施后的验收要求

- 先将两类红色探针变成稳定回归；覆盖同/不同 NodeID、旧命令迟到、新代接管、缺失/过期 epoch、正常覆盖和幂等解绑。
- 区分已失权进程与仍有效的旧业务 owner；覆盖改绑后的迟到保活、同 ID 重启继承、无请求但业务仍活跃，以及本地失效早于存储失效的窗口。
- 核对当前所有 Locator 实现与调用方；测试取消后结果未知、存储拒绝的错误身份、正常 Drain、停止超时，以及请求和业务 Session 契约。
- 用真实 Redis 与多节点 Cluster 验证 Lua、slot、恢复和故障窗口；单节点原型不替代这些验收。
- 按规则运行受影响包测试/race、make check/lint，复审完整 diff、同步文档后独立提交。只有修改权安全与经确认的重启/改绑语义同时成立才能关闭 I48。
