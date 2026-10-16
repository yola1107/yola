# 任务组织审核

审核日期：2026-10-16。基线：`ec6049a547bb6de532ff96e0f008d7928a7d9cbf` 加当前工作区。开始时有 30 个已修改文件。审核新增两份报告；用户随后调用 setup 并确认草案，本次另补充了普通实施票约定。

范围为根 module、根示例与文档。搜索、代码分析和验证排除独立的 `test/`；文档指向该目录的链接没有继续展开。文中的“已完成”限于列出的实现与本地回归，不等于部署验收通过。

## 结论

本地 Markdown 工作流已接通。`docs/issues.md` 正确承担验收索引职责，当前三组事项都含外部条件；没有依据把它们整体列为生产代码修复。可以独立准备的实施工作是补齐 RenewNode 的拓扑管理用例，其完整验收仍依赖专用六节点集群。

当前没有 `.scratch/`，因此没有可以核验状态、依赖和完成记录的现存实施票。审核发现的普通实施票状态与证据记录缺口，已通过本次 setup 补齐；Wayfinding 保留自己的状态规则。架构候选见 [HTML 报告](architecture-review.html)，均尚未选定实施。

## Matt 工作流与本地配置

| 核对项 | 依据与判断 | 下一步 |
| --- | --- | --- |
| 任务存放位置 | `AGENTS.md:45` 指向 `docs/agents/issue-tracker.md:3`；Spec 放 `.scratch/<feature>/spec.md`，每票一个 `issues/NN-<slug>.md`，讨论追加 Comments。配置成立。 | 本次 setup 沿用现有目录与 tracker，只补状态约定。 |
| 验收索引 | `docs/agents/issue-tracker.md:10` 要求实施票引用验收条目；`docs/issues.md:3` 已反向链接本地约定。 | 保留索引的触发条件和完成标准；后续票写增量交付物与证据，不复制整张清单。 |
| 当前实施队列 | 包含隐藏、忽略文件的定向检查确认 `.scratch/` 不存在。不能推出“任务丢失”，也不能把工作区改动视为已关闭的票。 | 下次选择具体工作时才创建相应目录和票；本次不伪造历史任务。 |
| 普通实施状态 | `to-tickets/SKILL.md:69` 的本地模板给出 `Status: ready-for-agent` 和 `Blocked by`；审核开始时本地仅定义 Wayfinding 状态。经用户确认，已在 `docs/agents/issue-tracker.md:13` 补齐普通票状态、领取、证据与完成规则。 | 后续普通实施票使用新约定；票内约定验证未完成时保持未结，独立部署验收由索引继续跟踪。 |
| Wayfinding | `docs/agents/issue-tracker.md:22` 已规定 map、Type、领取、Answer 和 resolved；共同依赖规则集中在第 11 行。上游 Skill 的 labels/assignee/native blockers 在此由本地字段替代。 | 保留原规则；只在目标或决策链足够模糊时使用，当前三项明确的验收条件无需另建大地图。 |
| Triage | 可用技能目录及 `/Users/nm/.codex/skills`、`/Users/nm/.agents/skills` 均未发现 triage Skill。setup 明确在 triage 未安装时跳过标签配置。 | 缺少 `triage-labels.md` 不算配置缺陷，不为本次审核安装工具或补标签。 |
| 领域文档 | `docs/agents/domain.md:3` 采用单一上下文；根 `GLOSSARY.md`、`docs/adr/` 不存在；第 8 行允许直接继续、按需创建。 | 本次沿用架构契约中的 Gateway、Node、Session、GateBinding、epoch、Drain 等名称。未来确认术语或作出值得记录的取舍时再创建领域文档。 |
| 验证与提交 | `implement/SKILL.md:13`、`:17` 的全量测试、自动提交约定受本仓库范围和授权覆盖；`AGENTS.md:11`、`:13`、`:14` 是本地依据。`Makefile:30`、`:49` 会进入 `test/`。 | 后续实施按授权范围选包检查并复审；没有明确授权不 commit/push。本次不运行 make check/race。 |

建议路径：对已确定的小任务直接使用 `/implement`；涉及多个会话时由 `/to-spec`、`/to-tickets` 形成可独立验证、带阻塞关系的票，再逐票实施。只有已有完整 Spec 和任务图时，`/implement-spec` 才有可调度的工作。候选架构方向先通过 `/grill-with-docs` 明确取舍，再决定是否立项；报告不将架构候选视为代码实施授权。

以上判断已读取本地 ask-matt、setup、to-spec、to-tickets、implement、implement-spec、wayfinder、grill-with-docs、grilling、domain-modeling、tdd 和 code-review 的 Skill 正文。setup 已按用户确认补齐本地约定，未运行代码实施流程。主要来源：[本地 setup](/Users/nm/.agents/skills/setup-matt-pocock-skills/SKILL.md)、[本地 to-tickets](/Users/nm/.codex/skills/to-tickets/SKILL.md)、[本地 implement](/Users/nm/.codex/skills/implement/SKILL.md)。

## 当前验收项逐项分类

| 条目 | 分类 | 代码与文档依据 | 下一步和完成证据 |
| --- | --- | --- | --- |
| [I40 NATS 安全配置](../issues.md#i40-nats-安全配置) | 配置入口与本地回归已完成；部署验收待外部条件 | `docs/issues.md:7` 明确尚未部署联调。凭据/TLS 入口在 `event/nats/options.go:49`、`:56`，连接配置和异步错误观测在 `event/nats/bus.go:81`。ACL、订阅上限、重连重复拒绝有 `event/nats/async_error_test.go:19`、`:51`、`:75`。 | 选定 broker、账户、证书、ACL 后，记录允许/拒绝操作、异步日志、payload 与订阅上限的实际配置和结果。无需预先新增认证抽象；本地测试通过不能关闭此验收项。 |
| [I41 / I45 容量与延迟](../issues.md#i41-和-i45-容量与延迟) | 目标待澄清；测量待外部条件 | `docs/issues.md:15` 已合并两项；`docs/performance.md:34` 要求负载、机器、拓扑和 SLO。`gateway/broadcast.go:122`、`:135` 和 `event/nats/subscription.go:55` 提供停止、接纳或统计实现，不能据此推出安全容量。 | 先确认连接数、消息模型、p99/drop/SLO 与持续时长，再按性能文档建立基线。登录突发、稳态、慢连接、重连可分别留证；完整游戏在本次排除范围内，需另行授权和验收。无瓶颈证据不生成缓存、队列或批量 RPC 修复票。 |
| [Redis Cluster 拓扑变更](../issues.md#redis-cluster-拓扑变更) | 用例补齐可实施；超时原因待复现；整体验收待外部条件 | `docs/issues.md:25` 只记录组合超时，未确定归因。`locate/redis/cluster_test.go:32`、`:95` 有槽迁移和协作切主场景；`:54`、`:83`、`:167` 等断言涉及 Bind/Unbind，没有 RenewNode 调用。`:181` 要求显式开启六节点管理场景。 | 可准备下节的覆盖增量。若首版使用单机 Redis，此项不阻塞首版；若使用 Cluster 且要求切主/扩缩容，在全新专用六节点集群分别运行单场景和组合，保留步骤、拓扑、错误与清理结果，再判断夹具或实现。 |

## 可实施工作的最小切分

下面是建议立项范围，不是已创建或已批准的任务。

| 工作 | 当前可执行部分 | 依赖与结束条件 |
| --- | --- | --- |
| 补齐 RenewNode 拓扑管理覆盖 | 沿现有迁移/切主夹具增加条件续租行为：分裂 key 的迁移窗口、ASK/MOVED 恢复后、切主后、旧 epoch、改绑或缺失时不覆盖归属。断言依据为 `locate/redis/node.go:54`、`locate/redis/script.go:94`、`docs/architecture.md:129`。 | 编写与编译可先完成；运行通过依赖专用六节点集群。未运行时只记录“实现就绪、外部验收待执行”，不能宣称覆盖已验证。 |
| 复现迁移与切主组合超时 | 准备单场景、组合场景的运行记录和每阶段拓扑快照，复用 `locate/redis/cluster_test.go:181` 的环境要求与清理保护。 | 取得可复现失败之后，才按具体失败路径建立修复票；不预设 Redis adapter 有缺陷。 |

建议放入同一 `.scratch/redis-cluster-topology/` 功能目录，实施票引用 [Redis Cluster 拓扑变更](../issues.md#redis-cluster-拓扑变更)。若拆票，测试增量与环境准备可以并行；最终验收依赖两者完成。环境是外部前提，应单列说明，不能伪造成一个已经 resolved 的编号。

例如后续 `issues/01-renewnode-topology-coverage.md` 可引用 `../../../docs/issues.md#redis-cluster-拓扑变更`，记录该票特有的验证结果；索引仍只保存验收范围。若架构候选获选，则新建独立功能票，引用 HTML 候选；当前验收索引没有相应条目时不要硬挂 I40/I41，也不要把架构优化塞进部署清单。

## 已完成的能力及下一步

| 能力 | 证据 | 下一步 |
| --- | --- | --- |
| NATS 发布等待与取消后的 Payload 所有权 | 近期 `e132932`；`event/nats/bus.go:165` 交接前复制，`:211` 单 worker；`event/nats/publish_test.go:15`、`:79`、`:133` 覆盖截止、Close join 和复制。 | 保留这些行为回归；发布尝试开始后仍可能投递是已写明的契约，不追加“可靠重试”任务。 |
| 订阅停止有界等待与后台回收 | 近期 `55ecf43`；`event/nats/subscription.go:34`、`:159`、`:191`；`event/nats/subscription_test.go:16`、`:223` 覆盖原生清理阻塞及再次等待。 | 保留取消与完成的区别；`Bus.Close` 无 deadline、handler 不可同步等待自身退出是 `docs/eventbus.md:66` 的调用约束。 |
| Node 条件续租与原子 epoch 核验 | 近期 `8c70429`；`locate/redis/script.go:76`、`:94`；`locate/redis/node_test.go:25`；`node/session_test.go:74`。 | 不重开 Bind/Unbind/普通 RenewNode 的实现工作；仅补上表的拓扑管理验证。真实 Cluster 历史通过记录只作已有记录引用，本次未重跑。 |
| 请求、业务 Drain 与出站排空分阶段 | `node/lifecycle.go:123`；`node/binding_test.go:20`、`:98`；`node/push_test.go:196`、`:212`。 | 维持请求 → Drain → 出站 → epoch 释放的顺序；架构候选只讨论封装该约束，不改变业务生产者的所有权。 |
| 心跳与业务隔离，callback 终止顺序 | `network/internal/inbound/dispatcher.go:35`、`:66`；`internal/queue/queue.go:89`；`network/tcp/client_test.go:427`、`network/websocket/client_test.go:294`。 | 保留行为，若选择 HTML 中的 C1，再集中关闭协议；不能因为两处代码相似就统一全部 TCP/WS I/O。 |
| 广播 Payload 隔离与发送前 lease 检查 | `gateway/broadcast.go:34`；`gateway/session.go:212`；`gateway/broadcast_test.go:49`。 | 保留单批完成后再推进下一批的顺序，不从广播统计推断客户端已收到或容量达标。 |

这些能力的相关本地用例已在本次所列包级 race 测试中通过。开始时的 30 个未提交改动没有来源任务或完成记录可对照，本报告不将它们整体认定为“任务已完成”，也不据此要求提交。

## 待澄清项

| 决策 | 已知依据 | 下一步 |
| --- | --- | --- |
| 容量目标与允许损失 | `docs/issues.md:17`、`docs/eventbus.md:43`；在线 Pub/Sub 允许丢失。 | 部署负责人给出目标负载、持续时间、p99 与 drop 容忍度，保留数据面语义；随后才能定义验收通过。 |
| 首版是否要求 Cluster 拓扑变化 | `docs/issues.md:27` 对单机首版与 Cluster 有不同阻塞条件。 | 确认选型、切主/扩缩容需求和专用环境；不得把管理测试超时自动升级为首版发布阻塞。 |
| Session 两种含义的术语记录 | `gateway/session.go:87` 是物理连接状态；`node/session.go:14`、`:56` 是请求回程路由，可能被业务保存。`docs/architecture.md:239` 已说明旧 Session 不追随重连。 | 后续领域讨论时分别明确“Gateway Session”和“Node Session”；词汇表只记录概念，不复制生命周期实现说明。 |
| 选择哪个架构候选 | [HTML 报告](architecture-review.html) 的 C1–C4 都没有实施决定。 | 先选一个候选并确认兼容性与测试 seam，再决定任务粒度。推测候选需要新增证据，不直接转 ready-for-agent。 |

## 验证记录与缺口

运行环境：`go version go1.26.7 darwin/arm64`；`go.mod:3` 声明 Go 1.26.6。以下命令均在仓库根执行，明确清除对应外部验收开关；只运行已有测试，没有新增测试或改变源码。

```sh
env -u YOLA_NATS_URL -u YOLA_NATS_CAPACITY_PUBLISHER -u YOLA_ETCD_INTEGRATION \
  go test -race -count=1 -timeout=180s \
  ./event/... ./network/... ./internal/queue ./registry/etcd

env -u YOLA_REDIS_INTEGRATION -u YOLA_REDIS_CLUSTER_INTEGRATION \
  -u YOLA_REDIS_CLUSTER_ADMIN_INTEGRATION \
  go test -race -count=1 -timeout=180s \
  ./node ./gateway ./internal/gateclient ./locate/redis
```

合计 16 个包通过。复用现有回归，未另写低价值测试。gopls 仅用于定位源代码关系，证据行号以当前磁盘文件复核；其返回的工具链路径为 Go 1.27，与实际测试工具链不同，因此不把语义导航当作构建结果。

报告的本地链接、HTML 标签结构、锚点与源码行号范围已通过静态校验。已打开 HTML 文件；浏览器工具因本地文件协议限制拒绝读取页面，未完成自动视觉与 Mermaid 渲染检查。

未运行真实 Redis/etcd 联调、Redis Cluster 管理、生产 NATS 凭据/TLS/ACL、容量基准及长期 SLO；本地 NATS 用例使用嵌入 broker，不能替代实际部署。未检查 `test/`、未运行全仓库 make check/lint/race 或完整游戏。报告无协议或源码修改，未执行代码生成、依赖整理及 Buf 兼容性检查。现有包级通过只覆盖实际运行的场景，不能证明全部并发交错安全。

收尾时，29 个既有修改文件的 SHA-256 与初始记录一致；`AGENTS.md` 在审核期间仅移动了 import 规则的位置。本任务未写入该文件，保留当前版本。HEAD 与 `docs/issues.md` 未变，新增改动路径仅为两份报告和任务约定。

本次保留领域文档约定与全部既有改动。交付两份审核报告，并按用户确认更新 `docs/agents/issue-tracker.md`；AGENTS 已有正确入口，未另加重复导航。
