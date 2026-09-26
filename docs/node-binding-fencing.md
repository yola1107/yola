# Node binding 修改权

**状态：已实现。** I48 在 `af33b00` 完成；本文只保留当前契约、选择依据和历史验证边界。未选定的跨 slot 协调、仅单机支持和分区 authority 草案已移除，不作为待实施任务。I03 业务保活未完成，本轮行为等价清理不改变这些契约。

## 当前实现

[Session](../node/session.go) 从 Node 自己的 lease 取得 epoch，交给 [Redis Locator](../locate/redis/node.go)。Redis 以同 slot Lua 原子核验调用方 epoch，再执行 binding 写入；取消 context 不能撤销已经发送的命令。

| 对象或操作 | 当前契约 |
| --- | --- |
| Node binding key | `locate:node:{base64url(service)}:base64url(uid)`，RawURL 编码；值仍为 NodeID，TTL 仍为 6h |
| Node epoch key | `locate:node:epoch:{base64url(service)}:base64url(nodeID)`，与同 service 的 binding 共用 slot |
| Bind | epoch 匹配时覆盖 NodeID；有效进程之间保留 last-write-wins |
| Unbind | 先核验 epoch，再仅删除 NodeID 匹配的 binding；缺失或其他 NodeID 为幂等无操作 |
| 失权 | epoch 缺失/冲突返回可分类错误；Node 取消已接纳工作并关闭准入，迟到续租不能恢复失效身份 |
| 其他失败 | caller 取消、网络错误等不自动撤销整个 Node 的进程身份；超时不保证存储已回滚 |
| 原 ID 重启 | binding 保留 NodeID；新进程取得新 epoch 后继承路由，旧 epoch 迟到写拒绝 |
| Redis 模式 | 应用注入单机或 Cluster client，框架共用 Locator/Lua；同 service 的 Node 读写集中在一个 slot |
| Gate binding | 继续按 UID 分布，不参与 Node epoch/binding 事务 |
| 应用身份 | 唯一外层 Kratos App 与共享 Registry 保持原职责；不新增 App、运行器或业务状态 owner |

源定义见 [key 编码](../locate/redis/decode.go)、[Lua](../locate/redis/script.go)、[epoch 生命周期](../node/epoch.go)。Node key 布局已一次性切换，不保留旧格式 fallback 或双读双写，框架不自动删除旧数据。Redis 异步复制可能回退已确认数据，Lua 原子性不构成跨故障切主的不可回退保证。

## 联合契约边界

| 概念 | 权威来源 | 不提供的保证 |
| --- | --- | --- |
| 物理连接归属 | Redis GateBinding；Gateway 本地 lease 控制准入，BindingToken 区分连接事件 | 远端 Kick 为 best-effort，不保证同 UID 强单活 |
| Node 进程修改权 | Redis epoch key 与 Node 本地 epochLease 各承担存储核验和本地准入 | 本地取消与 Redis 失权不是同一原子时刻，不撤销已开始 I/O |
| 玩家业务绑定 | Redis NodeID；业务 owner 决定建立、改绑与释放 | 进程仍有效不代表仍拥有每个曾绑定的玩家；进程 epoch 不能代替业务 owner |

- A 仍有效但玩家已改绑 B 时，A 再 Bind 仍可覆盖 B；不能把重复 Bind 当作安全保活。当前测试业务只在首次入座和重连 Bind，超 6h 的安全保活仍缺失，见 [I03](./issues.md#i03)。
- 本地 lease 先失效而 Redis 旧 epoch 仍存在时，存储无法仅凭 epoch 值得知本地取消。不把本地失效描述为已撤销全部存储修改权。
- 已绑定 Stateful Forward 仍按 Gateway binding、Gateway epoch、Node binding 的顺序查询；修改权修复没有删除接收端 fencing，也没有增加时间缓存。
- requests 与 deliveries 分开排空：业务 Drain 期间仍能绑定和推送，返回前由业务停止生产者；不合并两个准入屏障。

## 迟到写入证据

以下为 I48 实施前的历史复现，不是本轮清理的新验证。真实 go-redis 客户端在 `net.Conn.Write` 入口阻塞旧命令；另一连接完成 epoch 交接和新 binding 后取消旧 lease，再释放已开始的 Write。

| 交错 | miniredis，3 次 | 专用 Redis 8.6.1，3 次 |
| --- | --- | --- |
| 旧 A Bind，B 已成为新绑定 | 3/3 覆盖回 A | 3/3 覆盖回 A |
| 旧 A Unbind，同 ID 新代 A 已绑定 | 3/3 删除新绑定 | 3/3 删除新绑定 |
| 旧 A Unbind，B 已成为新绑定（对照） | 3/3 保留 B | 3/3 保留 B |

探针中的旧 context 已取消，写入仍返回 nil。这证明已开始的写入不能只靠取消保护；不意味着所有取消调用都会执行成功。环境与清理记录见 [I48 历史调查](./refactor-progress.md#i48-历史调查环境与结果实施前)。

在实施前基线 `0883c00` 的根目录执行 `go test -overlay <overlay.json> ./node -run '^TestAuditI48' -count=3 -timeout=30s -v`。未设置 `YOLA_REDIS_INTEGRATION` 时使用 miniredis；历史真依赖运行指向专用 `127.0.0.1:16379`、Redis DB 9 和随机 service。同筛选加 `-race` 仍为两类功能失败，未报告 data race，不计为修复验收通过。

历史产物位于 `%TEMP%\yola-i48-20260925`：`probe_test.go`、`overlay.json`、`miniredis-red.log`、`redis-red.log`、`redis-race-red.log`。探针 SHA-256 为 `3a61fa7149d4e2cb4c55af5468a835aa599e51dc299a52d9b018a967a110e775`；这些临时文件不保证长期保留，环境变化后须重建专用实例并按 checkout 调整 overlay。

## 已完成验证及限制

- 修复后单机和 Cluster 的真正迟到写、缺失/过期/冲突 epoch、正常覆盖与幂等解绑、原 ID 重启、Node 失权/迟到续租/Drain、Registry/Gateway 回归通过；当批 check/lint 与适用 race 通过。
- Migration、CooperativeFailover 各 3 个独立新建集群的 race 样本通过；连续合跑仍有 manual failover timeout，该组合未通过。首次旧夹具恢复触发的 Redis 断言和后续超时保留在 [完整验证记录](./refactor-progress.md#i48-results)。
- 上述结果限定原代码、工具和环境。本轮不重做 Cluster 管理操作，不把历史通过作为当前检查，也不关闭 I03 或扩大 Gate 单活保证。
