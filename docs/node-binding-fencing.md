# Node binding 修改权设计调查

状态：**I48 待设计，未实施迁移**。2026-09-25 基于 `0883c00` 核查；本文件是具体方案与复现证据，不授权更改 Locator API、key 布局或 binding 格式。当前用户优先处理代码架构，没有容量 SLO。

## 已确认的边界

- NodeID 是稳定路由目标；同 ID 新进程可继承原有定位。binding 由业务显式 Bind/Unbind 维护，TTL 为 6h，不随 Gate 连接关闭或进程 epoch 直接删除。
- 有效 Node 之间保留首请求 last-write-wins；本问题只禁止已经失去进程修改权的写入，不引入 actor 唯一性或跨 Gateway 强单活。
- `node.Server` 持有本代 epoch，`requestSession` 使用时核验本地 identity/lease，已接纳副作用纳入 deliveries 排空。I49 的屏障不能撤回已进入 socket Write 的命令。
- Redis 拥有最终存储修改权。当前 [Session](../node/session.go#L53) 未把 epoch 传给 Locator；[BindNode](../locate/redis/node.go#L15) 直接 SET，[UnbindNode](../locate/redis/node.go#L39) 只比较 NodeID。不能通过再加一次本地检查或写入后的检查，修复存储中的迟到覆盖。

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
| A：同 service 的 Node epoch 与 Node binding 共用 slot | Node 传本代 epoch；Redis Lua 在同一次操作中比较当前 epoch，再 SET 或按 NodeID 删除。继续只存一份 epoch 和一份 binding | 最少新增状态与协调；Node 定位集中到 service 的单个 slot，改变 Node key 布局。建议优先评估，须明确接受该分片约束后再实施 |
| B：保留当前 UID 分片并提供跨 slot 协调 | epoch 与 binding 仍由不同 slot 持有，需新增协调或复制修改权，并处理失效/恢复一致性 | 不能用两次 GET/SET 或事务外校验冒充原子性。尚无收益证据支持这套协调成本，不建议提前建设 |
| C：保留 key 布局，仅支持 standalone Redis 的多 key Lua | 单 Redis 节点可直接原子核验两 key | 代码较少但收窄部署契约，Cluster 仍会拒绝；不能作为行为等价的小修，也不以本地 Docker 现状默认采纳 |

只把 binding 值扩为 NodeID/epoch，不在写入时核验当前修改者，仍无法阻止旧 Bind。只为 Unbind 比较值也不闭合问题。因此 A **不需要改变 binding 的值格式**：保持 NodeID 字符串即可支持原 ID 重启继承；关键变更是修改命令携带 epoch，并让其核验与写入共处原子边界。

## A 的具体变更边界（候选）

1. 保持 `node.Session.BindNode/UnbindNode`、`LocateNode` 以及集群协议不变；框架从自己的 lease 取得本代 epoch，不能由业务传入或伪造。Locator 的 Bind/Unbind 增加 epoch 参数，adapter 只执行约定的存储条件，不持有业务生命周期。
2. Node binding key 候选为 `locate:node:{base64(service)}:base64(uid)`；epoch key 为 `locate:node:epoch:{base64(service)}:base64(nodeID)`。Gate key 不变；Node binding 仍为 NodeID、TTL 仍为 6h，不新增 UID 索引、缓存、续租 manager 或第二份授权状态。
3. Lua 先比较 epoch key 与调用方 epoch；不存在/不匹配拒绝且不改 binding。匹配时 Bind 保持覆盖写；Unbind 保持“仅当前 NodeID 匹配才删”。Node 侧沿既有 epoch 失效入口处理确定的失权，不能重试为无条件写入。
4. 新部署需一次性切换 Node key 规则；不做自动 fallback 或临时双读双写。即使项目尚未上线，也须先确定旧本地数据处理方式和 Cluster 分片约束，不擅自删除数据。

单节点 Redis Cluster 原型已分配全部 16,384 slots，使用无业务数据的任务专用容器。现有 `game/player` binding 与 `game/node-a` epoch 的 slot 分别为 12689、5067，多 key Lua 返回 `CROSSSLOT`；候选 service tag 的两 key 均为 6294。原型先设置当前 epoch 为 new，再分别以 old 执行 Bind/Unbind：均拒绝且新绑定保留；new 执行匹配 Unbind 成功。这里只验证 Redis 原子原语和 slot 约束，不证明完整 Locator、应用迁移、多节点 Cluster failover 或分区安全已完成。

## 实施后的验收要求

- 先将两类红色探针变成稳定回归；覆盖同/不同 NodeID、旧命令迟到、新代接管、缺失/过期 epoch、正常覆盖和幂等解绑。
- 核对当前所有 Locator 实现与调用方；测试取消后结果未知、存储拒绝的错误身份、正常 Drain、停止超时，以及请求和业务 Session 契约。
- 用真实 Redis 与多节点 Cluster 验证 Lua、slot、恢复和故障窗口；单节点原型不替代这些验收。
- 按规则运行受影响包测试/race、make check/lint，复审完整 diff、同步文档后独立提交。只有修改权安全与经确认的重启/改绑语义同时成立才能关闭 I48。
