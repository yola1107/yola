# 架构审查与修复进度

本文件是后续会话的执行交接入口，只维护阶段、验证证据和下一步；问题事实、候选方案和关闭条件见 [issues.md](./issues.md)。
恢复工作时以当前 Git、代码及本表为准；历史对话、静态审查和候选方案均不证明修复已经完成。

## 当前快照

- **更新日期**：2026-09-26。
- **代码审查基线**：起点 `0c8b270`；初始化交接文档 `9378b54`，I49 修复 `1a882f6`，I47 修复 `eb44302`，I52 修复 `a888fcb`，I51 修复 `1ed763b`，I50 修复 `1bacab6`，I46 框架预算修复 `7c12592`，I53 修复 `731bd1f`，I54 契约闭环 `2cc1743`，I55 修复 `0883c00`，I48 调查 `57a9dfc`，组件边界 `cf85f6e`，I46/I45 验收入口修复 `b1976ed`，根链路生命周期验收 `eb182b0`，I44 验收 `70e1777`。本批从 `70e1777` 完成 I48，实际 HEAD 以 Git 为准。
- **最近代码验证（I48）**：单机/Cluster 的 epoch 交接、真正迟到写、Node 失权与 Registry/Gateway 回归、make check/lint 和适用包 race 通过。Migration、CooperativeFailover 各 3 个独立新建集群的 race 样本通过；连续合跑 race 仍有 manual failover timeout，该组合未通过，详见 [验证记录](#i48-results)。
- **工作重点**：根 module 的架构与契约；`test` 是扩展与验收，不再以游戏局部重构替代框架分析。
- **Git 边界**：用户授权每个已解决 issue 独立提交，后续由用户统一审核；仅提交复审过的任务改动，不要求逐项 /plan 或 /clear。未授权 push 或发布。
- **当前范围**：外层原生 Kratos App 是唯一应用，Gate/Node 是内嵌组件；不新增 App/Config、运行器或掩盖原生覆盖关系的组合入口。固定 Kratos v3.0.0，不修改官方源码；保留唯一共享 Registry 与条件注册，不重做已关闭问题。用户已确认 I48 采用 service 分片方案，同时支持单机与 Cluster，不做旧格式兼容；I03 业务保活仍独立设计。
- **清单边界**：用户确认项目未上线、仅用于本地 Docker 开发，明确要求删除 I07；不恢复，也不将删除视为验收通过。
- **执行状态**：用户本轮要求先提交，不处理后续问题；I48 收尾提交后停止。20 项中 10 项已关闭，剩余 I03、I04、I08、I29、I34、I36、I40、I41、I45、I46 共 10 项。
- **后续候选顺序（尚未执行）**：先修已复现的 I46 Redis deadline 缺口，再推进 I04/I34 实测、I41 和完整业务验收；I03 保活不由进程 epoch 代替。用户暂无容量 SLO，只报告曲线和失败边界，不自行宣布安全容量。

## 状态口径

- `待复现`：已有静态证据，故障交错尚未运行验证。
- `待设计`：需明确边界或比较方案，不能把候选方案当作实现命令。
- `待实现`：方案与必要决策已明确，代码尚未完成；`实施中` 表示已开始修改。
- `待验证`：实现或基线已有，但关闭条件尚未满足；`约束` 表示继续遵守现有限制或由部署所有者验收。
- `已关闭`：修复、必要测试、完整 diff 复审均完成，记录提交/工作树证据与剩余限制；只完成文档不关闭问题。部分完成的父 issue 不划线，仅在两份文档同步划掉已完成子项，保留提交与验证记录。

## 问题进度表

| 问题 | 当前阶段 | 已有证据与验证边界 | 下一步 / 关闭记录 |
| --- | --- | --- | --- |
| ~~[I47 就绪与注册](./issues.md#i47)~~ | 已关闭（eb44302） | 原问题各复现 3/3；就绪适配、空键事务和本代 lease 注销已通过真依赖/race；完整 diff 已复审 | 唯一共享 Registry，官方 Discovery/Session；同 ID 旧记录未回收则冲突，lease 丢失后需重建应用 |
| ~~[I49 Session 副作用排空](./issues.md#i49)~~ | 已关闭（1a882f6） | 保存 Session 的后台 Bind/Unbind 已接入现有 deliveries；定向、包测试、race、lint 通过并复审 | Drain 内可操作；停止后拒绝；超时/重复 Stop 不释放 epoch；不解决 I48 |
| ~~[I48 binding 代次保护](./issues.md#i48)~~ | 已关闭（本批提交） | 同 service slot Lua 原子核验；单机/Cluster 迟到写、生命周期及适用 check/lint/race 通过 | [验证与限制](#i48-results)；连续快速管理操作组合 race 未通过，不关闭 I03/I36，不承诺异步复制不回退 |
| ~~[I51 NATS 激活归属](./issues.md#i51)~~ | 已关闭（1ed763b） | 五种原故障各 3/3；新契约定向 20 轮、包 race、check、lint 通过 | 经确认取消同步 ACL/上限承诺；每个异步错误独立记录，不再使用 LastError；同步失败仍终态 |
| ~~[I52 等待注册取消](./issues.md#i52)~~ | 已关闭（a888fcb） | 原实现失败 3/3；屏障回归 20 轮、包测试/race、lint 通过 | 获锁后重查 caller context；未发 SUB，第三次成功；激活后终态及 Close 语义不变 |
| ~~[I50 心跳调度](./issues.md#i50)~~ | 已关闭（1bacab6） | 两种 transport 各复现 3/3；定向 10 轮、默认周期、FIFO/认证/过载/续租/关闭、内存及最终 race/check/lint 通过 | 仅显式能力启用有界业务 FIFO 与独立心跳；排队满关闭，普通自定义 handler 仍串行；I46/I41 另验收 |
| [I46 预算所有者](./issues.md#i46) | 待修复与验证 | 预算分离与根链路子项完成；真实 go-redis 默认不把 caller deadline 用于 socket I/O，探针失败 3/3 | 后续先修 client 装配和错误分类；完整游戏副作用窗口、目标规模与稳态仍待验收，不提前划线 |
| ~~[I53 command 元数据](./issues.md#i53)~~ | 已关闭（731bd1f） | 原 dispatch 缺失 3/3；共享请求类型的真实 gRPC 20 轮、Node race、make check/lint 通过 | 只读 CommandFromContext，operation、Session、顺序及错误身份保持；无重复路由状态 |
| ~~[I54 消息所有权](./issues.md#i54)~~ | 已关闭（2cc1743） | TCP 原地修改探针失败 3/3、WS 对照通过；库内调用方审计完成；不可变消息真实连接/race 各 20 轮、最终 check/lint 通过 | 统一不可变输入契约，Prepared.Reset 不恢复写入权；运行实现不变，违规写入仍可能导致内容变化或 race |
| ~~[I55 在线容量观测](./issues.md#i55)~~ | 已关闭（0883c00） | 本地分层观测、关闭/取消/并发读、定向 race 20 轮及五包 race、check/lint 通过 | 可选只读能力；复用 BroadcastStats；原生最终 drop 未知、逻辑积压非 RSS；三组成本见 I55 记录 |
| [I36 Gate 强单活](./issues.md#i36) | 待设计 | 现有 best-effort Kick 的已知限制 | 独立决策：确认是否要求强单活及已开始操作边界 |
| [I40 NATS 生产安全](./issues.md#i40) | 约束 | 2026-08-20 开发环境观测，非当前环境结论 | 获准后核查目标 broker，验收认证、mTLS 与 ACL |
| [I03 binding TTL](./issues.md#i03) | 待设计（联合） | 固定 6h；Bind 是覆盖写，不能直接作为条件保活，测试业务暂无长业务保活实现 | 与 I48 区分进程修改权和业务绑定归属，确定保活/改绑契约后再实现 |
| [I08 sticky 切换](./issues.md#i08) | 约束 | 首次模式固定，后续变化 fail closed | 保持重启约定；有在线迁移需求后再设计 |
| [I29 可信代理 IP](./issues.md#i29) | 约束 | 当前使用 socket peer | 代理需求明确后设计并验收信任边界 |
| [I41 真实连接容量](./issues.md#i41) | 待验证 | 只有部分进程内分配收益，五档真实容量未关闭 | B6：I55 就绪后执行单 Gateway 五档验收 |
| ~~[I44 NATS 排队内存](./issues.md#i44)~~ | 已关闭（70e1777） | 指定 64 KiB/1 MiB broker 下 18 个独立样本、真实上限协议、适用 race/lint 通过 | [内存、拒绝、延迟与释放](./performance.md#i44-capacity)；不改默认队列，不承诺任意负载或立即归还 RSS |
| [I04 三次 Redis 查询](./issues.md#i04) | 约束 | 已绑定 Stateful Forward 的三次查询职责明确，远程查询未减少 | 测量可独立；删除查询、合并 fencing/保活或改变布局才依赖 I48/I03 契约 |
| [I34 续租波次](./issues.md#i34) | 约束 | 无跨 Session 整形，真实故障容量未验收 | B6：区分调度阻塞与 Redis 拥塞，量化后再决定 jitter |
| [I45 Table Push 长尾](./issues.md#i45) | 待验证 | 共用夹具已改原生 App.Run 并区分历史/当前预算，小场景消息流匹配；长期稳态仍未关闭 | B6 后续扩展验收；保持桌内顺序，不以 smoke 代表容量 |

已完成子项保留如下，父 issue 的状态以上表为准：

| 已完成子项 | 所属问题 | 提交与证据 |
| --- | --- | --- |
| ~~已完成子项：框架非请求预算分离~~ | I46 | `7c12592`；[B3 验证](#b3-results) |
| ~~已完成子项：原生应用与分层预算验收入口~~ | I46、I45 | `b1976ed`；[验收入口验证](#request-budget-fixture)，仍缺父问题的完整关闭条件 |
| ~~已完成子项：真实连接取消与已开始任务排空~~ | I46 | `eb182b0`；[生命周期验收](#request-lifecycle-results)，不替代完整游戏或存储故障验收 |

## 建议批次与实施边界

以下按职责安排当前顺序；B1～B6 保留历史批次对应关系，已完成内容不重新实施。

1. **接入边界（本轮文档补全）**：保留原生 Kratos App，明确组件、业务、依赖创建者和 endpoints 的职责；补充失败回收限制并修正过期口径。不新增应用包装，也不以减少 Option 行数为重构目标。
2. **绑定所有权（B4，I48 已完成）**：Node binding/epoch 按 service 同 slot，单机和 Cluster 共用原子操作。I48 的进程保护独立交付；I03 仍需业务 owner 的保活契约，I36 保持当前 best-effort。各问题分别保留关闭证据。
3. **验收入口与根链路（I46/I45，部分完成）**：已接入原生 App.Run、分别表达三段预算，完成真实 WS→gRPC deadline 与当前预算 smoke/robots；本批补齐 TCP/WS 已开始副作用、停止与重连收尾。后续验收完整游戏对应窗口、目标规模与稳态，不把小场景通过写成整项关闭。
4. **分层诊断（B6）**：I44 的指定配置订阅验收已关闭；I04 查询、I34 续租、I41 发送继续各自测量，复用现有 owner 与 I55 观测，有具体成本证据后才优化。I40/I29/I08 保留部署与行为选择边界。

| 批次 | 目标与收益 | 风险 / 依赖 | 验证出口 |
| --- | --- | --- | --- |
| B1（已完成） | I47、I49：实例就绪发布与框架副作用排空 | I48 单独处理；就绪屏障不能冒充完整跨存储原子性 | 既有 App 生命周期交错、排空与真依赖验证记录保留 |
| B2（已完成） | I52、I51：取消者无底层副作用；异步错误独立报告 | I51 经确认改为原生异步边界，保留单连接和同步失败终态 | 定向 20 轮、真实 ACL/上限/重连、协议交错、Close、race、check、lint |
| B3（框架修复完成） | I50、I46：连接存活与请求/清理预算职责 | I50 已关闭；I46 框架预算分离已验收，完整业务负载待 I45/B6 | 真实 TCP/WS/gRPC、慢请求、顺序、停止与预算功能已通过；保留业务容量未完成项 |
| B4（I48 已完成） | I48/I03：进程修改权、业务绑定和安全保活 | I48 的进程保护已验收；I03 业务 owner 继续设计，不加无依据的保活 manager | I48 已保留迟到写、重启、失权及真实 Cluster 证据；I03 保活另验收 |
| B5（已完成） | I53、I54、I55：补齐扩展接口与观测 | 低至中；按独立职责拆分原子改动，不建通用管理层 | command 区分、消息所有权/race、分层统计及热路径成本已验收 |
| B6（I44 已完成） | I41、I04、I34，再按需 I45：容量与优化验收 | 先有观测及固定预算；生产部署约束另行处理 | I44 已形成独立基线，其余按固定代码/配置/资源记录 p99、drop、RSS 与资源归还 |

<a id="next-actions"></a>
### 后续执行清单

本轮提交 I48 后按用户要求停止；下表未完成项仅作为恢复工作的顺序，不在本轮继续实施。

| 顺序 / 状态 | 操作与边界 | 交付与进入下一步的条件 |
| --- | --- | --- |
| 1 · 已完成子项 | 真实 TCP/WS→Gateway→Node 的本地超时/取消、迟到回复、远端取消后任务完成、停止和重连收尾；保留原有完成语义 | [10 个屏障场景及验证](#request-lifecycle-results)；使用 miniredis/静态 Discovery，不代表完整业务或真实存储故障验收 |
| 2 · 已完成 | I48 按已批准 A 方案核验 epoch 后原子 Bind/Unbind；保留 LWW、原 ID 重启继承、唯一 Registry | [关闭证据](#i48-results)；不做旧格式兼容、不暗中增强 I36，I03 独立设计 |
| 3 · 待恢复后实施 | I46 先在 Redis client 创建方启用 caller deadline，并在 Locator 的网络超时错误边界保留 context 分类；再测 I04 查询/pool wait、I34 调度/续租及 I41 连接容量 | 不修改共享 client 配置，不覆盖成功结果或 fencing 错误，不把取消当外部写回滚；固定代码/配置/资源后测量 |
| 4 · 前置验收齐备后 | 使用新夹具的明确预算，分别执行目标规模入座、固定到达率、完整对局与长期稳态，记录客户端失败窗口和逐 UID 消息结果 | 数据能对应真实预算与负载；没有业务 SLO 时只报告曲线和失败边界，不宣布安全容量，不提前关闭 I46/I45/I41 |
| 5 · 部署需求明确后 | I40 安全、I29 代理信任、I08 模式迁移分别验收 | 不把本地 Docker 条件外推为生产保证，也不无需求新增框架机制 |

每个原子改动按“复现 → 调用链与契约核对 → 最小实现 → 适用验证 → 完整 diff 复审 → 两份文档同步 → 独立提交”执行。整项满足关闭条件才为父 issue 划线；功能重合的按同一职责批次处理，各 issue 分别保留验收结论。

## 每批交接与验证记录

- 开始前检查 `git status --short`、未暂存/暂存 diff 与新增文件；重新核对当前 HEAD 和适用指令。已经完成的改动不重做，用户已有改动不回退。
- Bug 修复优先补稳定回归，不用长 sleep 猜竞争时序。根包测试观察框架契约，替身不代替真实 Redis/etcd/NATS 语义验收。
- Go 改动按 AGENTS 执行格式化、受影响包测试及 `make lint`；多包/跨 module/公共 API/依赖改动执行 `make check`；并发/连接/生命周期执行受影响包 `go test -race`；入口执行相应 build，协议改动执行指定基线的 breaking。
- 不重复 `make check` 已覆盖的同配置测试；它不替代 lint、race、breaking。文档改动只核对链接、命令、约束和 diff，不为此运行全量测试。
- 每解决一项问题，完成验证和完整 diff 复审，同步两份文档并划线，按问题单独提交，再核对下一项的依赖和验收方式。
- 每项实际工作记录：代码基线或提交、修改范围、原始失败与修复后结果、准确命令及执行目录、工具/环境、日志位置、未运行项、剩余风险和下一步。部分验证不得写成通过。
- 关闭条目时，先满足 issues 的关闭条件，更新架构/接入文档，在 issues 保留原条目并划线，本表同步划掉问题标题并保留关闭记录；不得删除已关闭条目，未完成的迁移不能标为行为等价重构。

| 日期 | 范围 | 实际完成 | 验证结果 / 剩余工作 |
| --- | --- | --- | --- |
| 2026-09-25 | 根 module 架构审查 | 对照 docs、代码、固定依赖及现有测试，记录 I47～I55，扩展 I46 | 静态审查；新增交错均未复现、未修复 |
| 2026-09-25 | 文档交接 | 整理 21 个未关闭问题及候选方案，建立进度表和恢复提示词 | 根目录 PowerShell 校验相对链接/锚点/行号、ID 与字段通过；git diff --check、git diff --cached --check 通过；未运行新 Go 检查，无新提交 |
| 2026-09-25 | B1 / I49 | 接入已有出站排空计数，新增保存 Session 后的后台操作回归；同步更新生命周期文档 | 原实现稳定失败；修复后定向 20 次、Node 包测试/race 和两个 module lint 通过；见下方命令 |
| 2026-09-25 | B1 / I47、I48 | I47 真依赖复现、用户确认冲突语义后实施；原 test Registry 提升到根模块并合并资源所有者 | I47 已验收；I48 只核对存储/重启契约，未改代码，未声称已复现 |
| 2026-09-25 | B2 / I52 | 获锁后补 caller context 校验，协议屏障验证取消者无底层副作用 | 复现 3/3；定向 20 轮、包测试/race、两个 module lint 通过；完整 diff 及调用链已复审 |
| 2026-09-25 | B2 / I51 | 复现并核实依赖后，按用户确认改为本地注册+Flush、异步错误独立报告 | 原故障各 3/3；新契约定向 20 轮、包 race、check、lint 通过；完整 diff 和调用链已复审 |
| 2026-09-25 | 清单调整与窗口交接 | 按用户要求删除 I07，保留其他问题 ID；补录 I54 提交和 I55 基准，更新续接提示词 | 根目录 PowerShell 核对 93 个本地链接/锚点/行号、两份文档的 20 个 ID 和 7 个关闭标记通过；diff 检查通过。仅文档，未重跑 Go 检查；I55 未实施，I46 完整业务/负载验收仍未完成；提交 `docs(audit): 移除 I07 并更新后续交接` |
| 2026-09-26 | I46 根框架生命周期验收 | 补齐真实 TCP/WS 取消、迟到回复、已开始任务排空及重连收尾；完善 Client.Request 与架构文档的取消边界 | 10 个场景定向 race 20 轮、四包 race、check/lint 通过；完成子项同步划线，父 issue 保持待验证，见下方记录 |
| 2026-09-26 | B6 / I44 | 补齐外置 broker、独立发布进程、精确接收屏障和资源释放测量；关闭指定配置验收 | 18 个独立样本、Payload 协议边界、适用 race/lint 通过；保留原始失败和配置边界，见下方记录 |
| 2026-09-26 | B4 / I48 | 按用户确认的 service 分片实施原子 epoch 校验，保留业务 Session API 与原 ID 继承 | check/lint、适用 race 和真依赖回归通过；连续快速管理操作组合未通过，保留失败证据；本批提交后按用户要求停止 |

前序 `470a4fa`、`7959b58`、`a83ccb2`、`0c8b270` 已提交 mailbox/Whot 局部修复；它们不关闭本表新增架构问题。历史测试或性能文档不能直接证明后续代码通过，复用结果须核对源码、依赖、配置和环境均未变化。

### 原生 App 与组件边界复核

- 用户明确 Gate/Node 是嵌入外层原生 Kratos App 的组件。本轮在 `57a9dfc` 上核对两组件接口、内部 gRPC、Node handler 注册、Gateway resolver、Kratos options/Run/Stop 和入口装配。独立 App/Config 草案已撤下，从未提交；草案和临时组合 Option 探针不构成当前能力或验证结果，不恢复该方向。
- 补全同进程接口注入、跨进程普通 gRPC、内部 cluster RPC、metadata/transport 列表覆盖、发现 endpoint 角色与外部依赖关闭边界。固定版本的应用早退和显式注销失败仍由外层 owner 处理；不能由组件承诺回收任意外层 hook 或其他 transport。
- 修正三处旧 RPCTimeout 口径为当前 CleanupTimeout；将 BindNode 的覆盖写与条件保活区分，更新 I48/I03 联合设计及 I04 的真实依赖。未改变运行行为、标识符、协议、Registry 或存储布局，未新增或关闭 issue。
- 本轮验证限于代码事实、文档相对链接/锚点/行号、两份清单 ID/关闭标记和完整 diff 核对；未运行新的 Go 检查或外部环境。I46/I45 同配置业务、I41/I44 容量和 I48 修复验收仍未完成。
- 根目录 PowerShell 核对 171 个本地链接、20 个问题 ID 和 8 个关闭标记一致；`git diff --check`、`git diff --cached --check` 通过。复审补清了共享 usecase 的跨入口排空、发现 endpoint 不识别 RPC 角色，以及 StopTimeout 仅提供协作式 deadline 的边界。

<a id="request-budget-fixture"></a>
### ~~已完成子项：原生应用与分层预算验收入口~~（2026-09-26，b1976ed）

- 基线 `cf85f6e`。原 `startServer` 只将一个未运行的 Kratos App 放进 context，直接 BeforeStart/Start，再由 StartGame 手动登记；`TestStartServerUsesNativeApplication` 要求 BeforeStart 已看到 App.Run 构建的 endpoints，修复前失败 3/3。该复现证明夹具边界错误，不表示已有生产入口绕过了 App.Run。
- 仅修改 `test` 验收模块：使用原生 Kratos App.Run，等待 AfterStart 与 gRPC Ready，复用 Node 的就绪 Registrar。共享 Registry 只登记 Node，Gateway 只使用 Discovery；纯 Push/Socket 夹具仍传 nil Registrar，不新增 etcd 依赖。失败清理在启动前登记，始终取消自有 context、执行独立 Stop 并等待 Run；用子进程故障注入核对 BeforeStart/Register 失败的原始错误、listener 回收和未误注销。
- `RequestTimeouts` 明确 Transport/Forward/Node，逐项配置并记录。历史 `TestGameDelivery` 保留 Ludo 15/15/15s、Whot 3/3/3s；`TestTableAdmission` 保留 15s。新增 `TestConfiguredGameDelivery` 按 Gateway 当前 3/3s 和 Node YAML（Ludo 5s、Whot 3s）运行。未改业务预算、入座/改绑语义、协议、Registry 实现或生产 App；该输入类型仅为测试数据，不是应用 Config。
- test module：`go test ./internal/pushbench -run '^TestGameRequestTimeoutsAcrossWebSocketAndGRPC$' -count=3 -timeout=60s -v` 通过；三层分别缩短为 250ms，客户端通过真实 WS 收到 DeadlineExceeded，Node 观察到最短预算并响应取消。`go test -race ./internal/pushbench -run '^(TestStartServer.*|TestGameRequestTimeoutsAcrossWebSocketAndGRPC)$' -count=3 -timeout=90s` 通过，包含最终两类失败回收用例。
- test module：专用环境、`YOLA_GAME_DURATION=10s`、`-p=1` 顺序运行两款游戏当前预算 smoke/robots 和历史 smoke，均通过；逐 UID 消息数量与摘要匹配，当前预算机器人场景分别观察到 Ludo 2 次、Whot 1 次机器人动作。最终入口 `go test -race -p=1 ./ludo/tools/press ./whot/tools/press -run '^TestConfiguredGameDelivery$/(smoke|robots)$' -count=1 -timeout=5m` 通过。普通烟测日志使用初稿名称 TestGameDeliveryConfigured；最终改名避免旧子测试正则误命中，并以新名完成 race/check。历史命令已加首段 `$` 锚点。
- 外部地址清空后，`go test -race ./internal/pushbench ./ludo/tools/press ./whot/tools/press -count=1 -timeout=120s` 通过；根目录 `make check`、`make lint` 通过，两个 module 均 0 issues，无新增或存量告警。修改 Go 文件已按 `.golangci.yml` 格式化；无协议、服务入口、构建链或依赖变更，不触发 breaking/build。check 中外部测试的跳过不计为通过，真依赖证据来自上面的定向命令。
- 环境：Go 1.26.6 windows/amd64、golangci-lint 2.13.2，race 在命令内前置 MSYS2 GCC 路径；VM 专用 Redis 8.6.1、etcd 3.5.21，仅 VM 回环 16379/12379，经本轮 SSH 转发访问。容器分别限 0.5 CPU/128MiB、0.5 CPU/256MiB，使用 tmpfs、独占 Redis DB 0、随机 service/UID/etcd prefix。收尾核对任务 label、完整 ID、挂载和隧道参数后删除本轮两容器与隧道；未操作既有服务，etcd 测试 prefix 收尾查询为空。
- 日志为 `%TEMP%\yola-i46-fixture-cf85f6e` 下 `repro.log`、`local-tests.log`、`wire-timeouts.log`、`fixture-race.log`、`configured-games.log`、`historical-games.log`、`configured-race.log`、`package-race.log`、`check.log`、`lint.log`。计时样本未与编译/检查并行，不宣称性能优化。`GameProbe.Stop` 仍先排空 Node 验消息，App cleanup 后结束；关闭阶段可能出现已有 best-effort Disconnect 到已停止 Node 的告警。
- 完整 diff 已核对所有 startServer/StartGame 调用方、注册和资源关闭顺序；两份文档保留未关闭状态。当前结果不覆盖完整 YAML 部署、TCP 同场景、1,000 桌与长期 SLO；10s smoke 也不保证每桌完成完整对局。I46/I45 仍待验证，I48/I03 未实施迁移。

<a id="request-lifecycle-results"></a>
### ~~已完成子项：真实连接取消与已开始任务排空~~（2026-09-26，本批提交）

- 基线 `80b4084`，接手时工作树与暂存区均为空。沿 Request tracker、连接 dispatcher、Gateway Forward/Close、Node dispatch/Stop 核对后，未发现本窗口需要修改的生产行为；本批补齐缺失的跨层验收并澄清公共注释，不将它写成生产故障修复。
- 根 `gateway/request_lifecycle_test.go` 使用真实 TCP/WS Client、Gateway、Stateful Node 和原生 App.Run，Locator 为 miniredis、Discovery 为静态实例。每种 transport 包含本地 cancel/deadline、断连/Forward deadline 后排空、迟到连接清理，共 10 个场景。Client 请求预算 5s；本地/Forward 截断场景 250ms；服务端其余请求上限与 Node Stop 为 3s。
- 本地取消只移除本次等待，Node context 仍有效；客户端编码屏障确保第二 pending 已建立后才释放旧 handler，核对旧响应不串给下一请求。断连/Forward deadline 已取消 Node context 时，已开始副作用仍可完成；真实请求返回 Unavailable 证明停止准入已关闭，Drain 和 App.Run 仍等待 handler，完成一次后才排空并释放 epoch。
- 旧 Unbind 被屏障延迟，新认证和业务 Session 更新后才放行；旧 Disconnect 携带旧 token，测试业务 owner 在同一锁内比较 token 并忽略旧事件。新 Gate binding 与 Node binding 保留，保存的旧 Session.Push 返回 NotFound，新 Session.Push 到达客户端。该测试不证明跨 Gateway Kick 失败时强单活，也不覆盖 I48 的迟到 binding 写入。
- 初稿 `go test ./gateway -run 'Test(ClientCancellationLeavesStartedRequestRunning|CanceledForwardRemainsInNodeDrain|ReconnectSurvivesLateConnectionCleanup)$' -count=1 -timeout=60s` 通过。复审补强第二 pending 与旧回复重叠屏障后，最终 `go test -race ./gateway -run 'Test(ClientCancellationLeavesStartedRequestRunning|CanceledForwardRemainsInNodeDrain|ReconnectSurvivesLateConnectionCleanup)$' -count=20 -timeout=120s` 通过。未使用初稿的 race 结果代替最终检查。
- 根目录 `go test -race ./gateway ./node ./network/tcp ./network/websocket -timeout=180s`、`make check`、`make lint` 通过。lint 初次仅报告本批测试中的变量遮蔽，已修正；最终两个 module 均 0 issues，无存量告警。修改的三个 Go 文件已用 `golangci-lint fmt --config .golangci.yml` 格式化。无入口、依赖或协议变化，不触发 build/breaking。
- 工具为 Go 1.26.6 windows/amd64、golangci-lint 2.13.2；race 仅在命令内前置 `D:\soft\msys64\mingw64\bin`。本批未连接 VM、未创建外部容器，真实 Redis/etcd 环境变量未设置，check 中对应测试的跳过不计为验收通过。日志位于 `%TEMP%\yola-i46-lifecycle-80b4084`：`initial.log`、`targeted-race.log`（初稿）、`lint.log`（初次告警）、`final-targeted-race.log`、`final-lint.log`、`check.log`、`package-race.log`。
- 完整 diff 与相关调用链已复审，含测试屏障、错误身份、失败清理、资源交接及两处 Request 注释。这里只验收可控业务 handler，未运行完整 Ludo/Whot 副作用窗口、目标规模或稳态；不关闭 I46/I45，不改变 Registry、binding 布局、协议或已开始操作的完成语义。后续按 [执行清单](#next-actions) 继续联合设计与独立分层诊断。
- 根目录 PowerShell 核对三份修改文档的 126 个本地链接、两份清单的 20 个 issue ID、8 个关闭标记及 3 个完成子项一致；`git diff --check`、`git diff --cached --check` 通过。概览、当前阶段、详细证据和后续清单同步，不恢复 I07，不将本批子项完成写成父问题关闭。

<a id="i44-results"></a>
### I44 关闭记录（2026-09-26，本批提交）

- 基线 `eb182b0`。原积压基准仅等 `Dropped>0`、只测一个订阅并把业务上限设为被测 Payload，无法证明全部 overflow 已接收，也没有独立 RSS/关闭后样本。本批只改 `event/nats` 的测试和基准，复用 I55 统计；两个基准共用按接收计数分批充队列的屏障，未改变生产队列、状态所有者或默认参数。
- 预检 VM 的 4 vCPU、约 5.8 GiB 可用内存、已有五个服务、端口与挂载后，创建专用 NATS 2.10.29 两实例，分别为 64 KiB/1 MiB `max_payload`、64 MiB `max_pending`，各 0.5 CPU/256 MiB，端口仅绑定 VM 回环。每次接收容器限 2 CPU/1536 MiB、`GOMAXPROCS=2`、只读挂载二进制；独立 publisher 子进程不计入接收进程 RSS。
- 最终非 race 二进制 SHA-256 为 `3bcb2b098f90d832dce12ddee6f3b22776b933f406f83c77c9974cf297612f2d`。六格 `small/default/four/close_default/oversized/close_oversized` 各用 3 个独立进程，命令 `-test.run='^$' -test.bench='^BenchmarkSubscriptionCapacity$/^<格名>$' -test.benchtime=1x -test.count=1 -test.timeout=60s` 全部通过。前四格用 64 KiB broker，后两格用 1 MiB broker；各格按脚本顺序运行，容量结束后才在 VM 编译、运行 race，既有服务仍在运行。数值与模型限制见 [性能记录](./performance.md#i44-capacity)。
- 初稿 Linux 容量 race 的超限档未达到预期排队/拒绝计数：broker 日志证实 `MaxPending of 67108864 Exceeded` 后断开接收者，发布 Flush 不能证明接收方已经跟上。已改为每批最多 16 条并等待接收计数，不放宽 broker/队列；最终 Linux race 二进制分别在独立进程运行 `four` 和 `close_oversized`，均通过。初稿失败不计为通过，race 的内存开销不纳入容量表。
- `TestBrokerPayloadLimits` 在 Windows 内嵌 NATS 2.14.5 的两档上 `-count=3` 通过、`-race -count=20` 通过；Linux 两个真实 broker 各 3 轮通过，大 Payload broker 的真实边界另有 race 3 轮通过。原始 PUB 请求与 nats.go 预检分别断言，未将客户端本地拒绝冒充 broker 拒绝。
- 最终根目录 `go test ./event/nats -count=1 -timeout=60s`、`go test -race ./event/nats -count=1 -timeout=90s`、`go test ./event/nats -run '^$' -bench '^BenchmarkSubscriptionBacklogMemory$' -benchtime=1x -count=3 -timeout=60s`、`make lint` 通过，两个 module lint 为 0。初稿复杂度告警定位到积压控制与采样混在一起，两个基准复用同一充队列职责后消除，无 exclusion 或生产抽象。三个修改的 Go 文件已格式化；只改单包测试/基准，无 API、依赖、入口或协议变更，不触发 make check/build/breaking。
- 工具为 Go 1.26.6 windows/amd64 与 linux/amd64、golangci-lint 2.13.2。Windows race 在命令内前置 MSYS2 路径；Linux 使用 VM 已安装 Go/GCC 11.5，在任务源码和独立 build cache 下编译，`GOPROXY=off`，未安装或升级工具。
- 原始产物位于 `%TEMP%\yola-i44-eb182b0`。`verified-<格名>-<轮次>.log`、`verified-results.json`、`verified-binary.sha256` 为最终容量；`verified-race-four.log`、`verified-race-close_oversized.log`、`verified-package-race.log`、`verified-tests.log`、`verified-lint.log` 为最终检查。`capacity-race.log` 与 broker 日志保留初稿失败，早期 `probe/final/accepted` 样本不替代最终结果。`evidence.tar.gz` 包含最终源码、配置、服务端日志及清单，SHA-256 为 `a7f7e5ce835d6d71713b3ace2b6d062b97a0ff28c43dd15333527135b4eead16`。
- 归档下载及 SHA 校验后，核对完整容器 ID、任务 label、挂载和绝对目录，仅删除本轮两 broker 与 `/tmp/yola-i44-eb182b0` 的源码/build cache；任务标签下无残留容器。测量容器逐次自动删除，没有 SSH 隧道；未修改或清理既有五个服务。
- 完整 diff 按订阅所有权、消息计数、handler 退出、子进程清理及 RSS/heap 口径复审；I44 在两份清单同步划线。验收仅覆盖记录的 broker、PUB 消息和协作 handler，不关闭 I40/I41，不把强制 scavenging 的回落当作 Close 保证。
- 本批四份文档的 135 个本地链接、20 个 issue ID、9 个关闭标记和 3 个完成子项核对一致；diff 检查通过。I48 的设计草案留待其独立实现提交，本批只在交接中记录已获授权的下一步。

<a id="b1-results"></a>
## B1 本轮验证记录

- I49 验证时基线为 `0c8b270` + 当时工作树，现已提交为 `1a882f6`；实现范围为 `node/session.go` 的两处准入及对应职责注释，回归见 `node/session_lifecycle_test.go`。复审确认同步 handler、Drain 内操作、无 Locator 错误、epoch 失效取消和 Push 流程保持原契约；关闭后的绑定调用统一拒绝为 `Unavailable`。
- 根目录先执行 `go test ./node -run 'Test(StopWaitsForSavedSessionBinding|SessionBindingDrainTimeoutKeepsEpoch|BusinessDrainCanBindAndUnbindSavedSession|EpochLossCancelsSavedSessionBinding)$' -count=1 -timeout=30s`：原实现中两种后台写入均导致 Stop 提前成功，超时断言均失败。增加屏障后相同四组测试 `-count=20 -timeout=60s` 通过。
- `golangci-lint fmt --config .golangci.yml node/session.go node/server.go node/lifecycle.go node/session_lifecycle_test.go`；`go test ./node -count=1 -timeout=120s`；`go test -race ./node -count=1 -timeout=120s`；`make lint` 均通过。lint 初次发现新增测试 helper 的 context 参数顺序问题，已修正；最终根/test module 均为 0 issues，无存量告警。
- 工具：Go 1.26.6 windows/amd64、golangci-lint 2.13.2、MSYS2 GCC 15.2.0。首次 race 因 cgo 编译器运行失败未完成；最小 C 程序也失败，在当前命令内前置 `D:\soft\msys64\mingw64\bin` 到 PATH 后 C 编译和 race 均通过，未改机器持久环境。
- I47 隔离资源：VM `root@192.168.152.129`；本任务容器 `yola-arch-20260925-redis`、`yola-arch-20260925-etcd`，仅 VM 回环端口 16379/12379、tmpfs、无持久卷，SSH 转发到同名本机端口。Redis 使用 DB 9、随机 service，etcd 使用 `/yola/audit/20260925` 下随机 service；未操作原有 Redis/etcd/其他容器。
- I47 探针命令：设置 `YOLA_REDIS_INTEGRATION=127.0.0.1:16379`、`YOLA_ETCD_INTEGRATION=127.0.0.1:12379` 后，`go test ./node -run '^TestAudit(PreparedNodeMustNotOverwriteReplacement|OldDeregisterMustKeepReplacement)$' -count=3 -timeout=60s`。两组各失败 3/3，旧 Start 的 epoch conflict 与新 epoch 保留断言成立。屏障显式调度 Kratos 已允许的交错，缩短的仅是本任务 Redis key TTL，不是自然暂停时长测试。
- 探针已移出正常测试集，源码、`i47-overlay.json`、`i47-repro.log`、`b1-lint.log` 保存在本机 `%TEMP%\yola-arch-20260925`。需要复现时使用 `go test -overlay <该目录>/i47-overlay.json ./node -run '^TestAudit' -count=3 -timeout=60s`，先重新建立专用资源；不能把探针失败写为验收通过。
- I47 实现：`node/registry.go` 的就绪适配等待首次续租结果，登记 I/O 纳入 requests；`registry/etcd/registrar.go` 用空 key 事务和固定 lease 撤销保护所有权。共享 `Registry` 由原 test 包提升并统一持有 client/lease，原包及 examples 资源包装已删除；官方 `concurrency.Session` 管理续租，官方 Registry 仅用于 Discovery/Watch。应用工厂只接收 Kratos `registry.Registrar`；未新增通用 manager、跨存储协调或业务状态。
- 新增回归覆盖：初次续租阻塞/失败、准备后被同 ID 新代接手、Stop 与就绪/已接纳登记竞争、注册失败的 listener/epoch 回收、条件登记冲突、迟到事务、旧 lease 注销、提交成功但响应取消后的回滚、client 关闭及 15s TTL 回收。根包承担框架验收，两个应用入口保留装配失败清理测试。
- 最终命令（仓库根）：修改 Go 文件均已按 `.golangci.yml` 格式化；`make check`、`make lint`、`make build` 通过。专用环境及当前命令前置 MSYS2 PATH 下，`go test -race ./node ./registry/etcd ./examples/env ./gateway -count=1 -timeout=120s` 全部通过，含真实 Redis 8.6.1、etcd 3.5.21 回归。日志为 `%TEMP%\yola-arch-20260925\b1-final-check.log`、`b1-final-lint.log`、`b1-final-race.log`。
- 最终命令（test module）：`go test -race ./ludo/cmd/ludo-server ./whot/cmd/whot-server -count=1 -timeout=60s` 通过；Ludo/Whot 各目录的 `make build` 及 test 目录的 `go build -o ../bin/ ./gateway` 通过。调整 Wire 接口绑定后分别执行 `go generate ./cmd/ludo-server`、`go generate ./cmd/whot-server`，生成结果与原文件一致，无手改生成文件。
- 检查中间结果：初稿新增的空消费循环和测试换行 lint 告警已随复用 Session/格式修正清除，最终两个 module 无新增或存量告警。提升 Registry 后 `make check` 曾因 test/go.mod 的直接依赖标记失配失败；修正为 indirect 后完整重跑通过，未改变任何依赖版本。
- 边界：`make check` 未注入外部地址，外部测试的通过证据来自上面的独立 race 命令；完整游戏/容量压测、Redis Cluster、I48 迟到写入均未运行。未改协议，未运行 breaking。原 namespace、ServiceInstance 格式、NodeID、Redis binding 与路由不变；同 ID 冲突和 lease 丢失需重建应用是本轮已确认的注册行为，不宣称跨 Redis/etcd 原子性。
- 资源清理：核对完整容器 ID、任务 label、tmpfs 和回环端口后，仅删除本任务两个容器并关闭 PID 12052 的匹配 SSH 隧道。原有五个容器仍运行，未操作它们的数据、配置或挂载。探针源码/日志保留供复核，临时 C 编译探针程序已清理。
- 交付复审：覆盖全部暂存/未暂存/新增文件及相关装配、注册、停止调用链；提交前分别核对暂存区与工作树，未混入其他问题的代码。文档相对链接、锚点、行号范围校验通过，19 个未关闭问题加 2 个关闭记录与 21 行进度对应；`git diff --check`、`git diff --cached --check` 通过。按用户后续授权执行问题独立提交，未执行 push 或发布。

<a id="b2-results"></a>
## B2 验证记录

- I52 接手基线 `eb44302`，工作树、暂存区和新增文件均为空；独立提交 `a888fcb`：`fix(event): 避免等待注册取消污染订阅能力`。生产改动只在 `event/nats/event.go` 的注册锁内、底层激活前重查 caller context，保留 Bus Close 和历史终态错误优先级。
- 原实现运行 `go test ./event/nats -run '^TestSubscribeCanceledWhileWaitingDoesNotActivate$' -count=3 -timeout=30s`，取消污染断言失败 3/3。`activation_test.go` 通过真实 nats.go 连接和本机协议端扣住 PONG，让第一注册持锁，再取消已通过初检的第二注册；修复后连续 SID 和线上命令证明未创建第二个底层订阅。
- 格式化：`golangci-lint fmt --config .golangci.yml event/nats/event.go event/nats/activation_test.go`；定向：`go test ./event/nats -run '^Test(SubscribeCanceledWhileWaitingDoesNotActivate|SubscribeCancellationAfterActivationRemainsTerminal|CloseRejectsSubscriptionWaitingForRegistration)$' -count=20 -timeout=60s`，全部通过。
- `go test ./event/nats -count=1 -timeout=120s`、`go test -race ./event/nats -count=1 -timeout=120s`、`make lint` 全部通过；根/test module lint 均为 0 issues，无新增或存量告警。工具为 Go 1.26.6 windows/amd64、golangci-lint 2.13.2；race 仅在该命令前置 `D:\soft\msys64\mingw64\bin` 到 PATH。
- 测试只使用本机回环随机端口的协议夹具及依赖内嵌 NATS Server v2.14.5，不读外部测试地址，无 VM 资源；测试 cleanup 回收连接和服务器。I52 不改公开 API、依赖、入口、协议或跨包代码，未触发 make check、build、breaking。
- I52 限制：等待 mutex 的调用仍在获锁后返回取消；取消检查之后才发生的取消属于已进入激活的失败，仍终止注册能力。I51 的重复 ACL 和异步错误归属由下方独立记录闭环。

- I51 调查基线 `a888fcb`。`go test ./event/nats -run '^Test(SubscribeRejectsRepeatedPermissionErrorAfterReconnect|SubscribeActivationErrorCannotBeOverwritten|NATSFlushDoesNotWaitForAsyncErrors)$' -count=3 -timeout=60s`：重复 ACL、三种错误覆盖、其他 Topic ACL 误归共五种故障各 3/3；回调屏障能力断言通过。这是故障复现，不是验收通过。
- 原测试先成功订阅，关闭内嵌 broker，再在同一回环端口启动只允许另一 Topic 的临时 broker；真实自动重连重放 SUB 后产生相同 ACL 错误。其余协议夹具按顺序发送真实 NATS 错误帧；slow-consumer 由真实 nats.go 的容量 1 channel 接收两个 MSG 触发，没有手写或直接替换 LastError。
- `go test ./event/nats -run '^TestNATS(FlushDoesNotWaitForAsyncErrors|SubscriptionErrorCapabilities)$' -count=20 -timeout=60s` 及同筛选 `go test -race ./event/nats ... -count=20 -timeout=90s` 均通过。证明：权限状态可跨连接错误覆盖保留；上限拒绝不改变订阅 IsValid、NextMsg 仅超时；ACL 回调不带 subscription identity；回调被屏障阻塞时 Flush 和 Barrier 仍可完成。未将该结果写成完整包测试或 I51 修复通过。
- 探针已从正常测试集移到 `%TEMP%\yola-b2-20260925-a888fcb\activation_errors_test.go`，同目录保存 `i51-overlay.json`、`i51-repro.log`、`i51-capabilities.log`、`i51-capabilities-race.log`。复现原故障须使用基线 `a888fcb`，并按 checkout 路径调整 overlay 的 Replace key；命令为 `go test -overlay <该目录>/i51-overlay.json ./event/nats -run <上述筛选> ...`。在原基线已实际用 overlay 重跑能力探针 1 轮通过。本机随机端口测试，不需 VM；后续新契约验收不能继续使用旧同步承诺的断言。
- 实施前的新契约回归：`go test ./event/nats -run '^TestSubscribeReportsPermissionFailureAsynchronously$' -count=3 -timeout=30s` 在原实现失败 3/3（仍错误地同步返回 ACL）。修复后，`go test ./event/nats -run '^Test(Subscribe|CloseCancelsActiveSubscription|CloseRejectsSubscriptionWaitingForRegistration)' -count=20 -timeout=120s` 全部通过；日志为 `i51-new-contract-before.log`、`i51-targeted.log`。
- 实现只删除 LastError/文本猜测并安装无状态的原生 ErrorHandler，将原始 error 交给 slog，仅在底层提供 subscription 时附带 topic。未新增公开接口、连接、注册状态或日志队列；未改变 handler 消费、关闭及队列所有权。同步取消后的终态已扩展到 ForceReconnect 后复验，另覆盖 Flush timeout 与活动注册中的 Close。
- 修改的 `event/nats/event.go`、`subscription_test.go`、`async_error_test.go`、`activation_test.go` 已按 `.golangci.yml` 格式化。`make check`、`make lint`、`go test -race ./event/nats -count=1 -timeout=120s` 均通过；两个 module lint 为 0 issues，无新增/存量告警。公开行为调整执行了 make check，受影响包测试包含在其中，未无故重复同配置检查；日志为 `i51-check.log`、`i51-lint.log`、`i51-race.log`。
- check 在当前进程移除 Redis/etcd 集成地址，相关集成和完整游戏测试按入口条件跳过；这些跳过项不算通过。NATS ACL、Publish ACL、上限及重连由真实内嵌 v2.14.5 服务器验收，错误交错由协议屏障验收。无入口/构建链/协议变更，未触发 build、breaking；无 VM 资源待清理。临时 broker、socket 和测试日志替身由 cleanup 回收。
- I51 独立提交 `fix(event): 按异步边界报告 NATS 订阅错误`；复审覆盖完整 diff、新增测试、注册/消费/关闭调用链及全部 LastError 使用点。日志只用于异步诊断，不参与注册状态判断；原生错误回调不访问 Bus 锁或注册状态。文档保留原条目、同步划线并核对引用，未执行 push。

<a id="i51-design"></a>
### I51 已确认方案

原契约要求 Subscribe 同步返回本次订阅 ACL/上限失败，并排除其他订阅、Publish 和 slow-consumer。固定依赖的公共 API 不能提供完整、非破坏性的订阅错误读取或错误队列完成屏障；单连接下只有 Topic 的 ACL 帧和无 Topic/SID 的上限帧，也不能当作按 SID 确认。用户在明确这些影响和下表方案后要求继续，按原生异步边界实施。

| 方案 | 结果与成本 | 状态 |
| --- | --- | --- |
| 按原生异步边界修复（已采用） | 单连接、ChanSubscribe、有界 channel 和公共接口不变；删除 LastError 及文本比较。Subscribe 同步确认本地注册和 Flush，ACL/上限由 NATS 异步错误回调报告，不假称属于某次激活。同步激活/Flush/context 失败仍终态；ACL/上限不再保证使启动注册同步失败 | 已按用户继续指令实施；通过现有 slog 报告，无新状态或公共接口 |
| 保留全部同步保证 | 需要支持错误代次/归属与完成屏障的依赖 API，或另行承担协议/TLS/重连接入职责；现有 API 下采样回调、延时等待均不能证明完整性 | 未采用；未升级、fork、修改官方源码或增加协议包装 |

验收以已确认契约为准：ACL/上限独立异步报告，保留 I52 取消和同步失败终态；覆盖重复 ACL、Publish/slow-consumer 交错、既有订阅可用、Close/取消和错误可观测性。应用启动不能仅依靠 Subscribe 返回值证明 ACL/容量配置正确，I40 的部署权限验收仍是独立要求；不因此关闭 I40 或 I44，也不宣称恢复了原来的同步保证。

决策遵循用户“若现有依赖无法满足契约，先准备具体方案和证据，再确认必要的行为变化”：先交付故障及能力探针，再明确启动失败行为的变化；用户要求“继续后续流程”后实施。Kratos v3.0.0、nats.go v1.53.1 及其他依赖版本保持不变。

<a id="b3-results"></a>
## B3 验证记录

- I50 基线 `1ed763b`。新增 `gateway/heartbeat_lifecycle_test.go`：真实 TCP/WS 客户端使用 50ms ping，业务保持默认 3s 上限，由 gRPC Forward 屏障持续阻塞。`go test ./gateway -run '^TestSlowForwardDoesNotBlockHeartbeat$' -count=3 -timeout=30s` 两种 transport 各误断线 3/3；日志 `%TEMP%\yola-b3-20260925-1ed763b\i50-repro.log`。这是缩短心跳周期的确定性故障场景，不是默认 5s 心跳时长的容量验收。
- 实施方向：显式 `network.HeartbeatHandler` 能力使 Gateway 独立处理心跳，自定义普通 handler 保持串行；成功认证后启用一个业务 worker 和有界 FIFO，默认 8 个等待帧，TCP/WS 提供 RequestQueueSize 配置。队列或发送容量满时关闭过载连接；停止取消并等待在途处理，再调用 Close。Gateway 通过 heartbeatMu 保护续租与关闭交接，保留业务 handlerMu 和 bindingMu 的所有权。
- 初步验证：修复后的慢 Forward 测试 `-count=10 -timeout=45s` 通过；`go test ./network/internal/inbound ./network ./gateway ./network/tcp ./network/websocket -run 'Test(Dispatcher|Invoker|SlowForward|ServerRejectsInvalidConfiguration)' -count=3 -timeout=60s` 通过。当时仅完成部分验收，后续最终 lint/check/race 记录见下。
- 后续定向：`go test ./network/internal/inbound ./network ./gateway ./network/tcp ./network/websocket -run 'Test(Dispatcher|Invoker|SlowForward|Pipeline|HeartbeatBeforeAuthentication|ServerRejectsInvalidConfiguration)' -count=10 -timeout=90s` 通过。覆盖真实 pipeline 的业务顺序、心跳越过积压并续租、队列满丢弃未执行请求、认证先于业务及认证前 heartbeat 拒绝。队列满关闭前允许在途请求返回 Canceled，未将收到该响应误判为未关闭。
- 默认周期：`go test ./gateway -run '^TestDefaultHeartbeatSurvivesExtendedForward$' -count=1 -timeout=60s` 通过；TCP 使用 5s ping/15s 业务预算，WS 使用默认 15s ping/45s 业务预算，均在业务屏障未释放时完成两轮心跳。关闭/续租屏障：`go test -race ./gateway -run '^TestSessionCloseWaitsForConcurrentHeartbeatRenewal$' -count=20 -timeout=30s` 通过，Close 等待被阻塞的 Renew 后撤下 binding。
- 新增队列容量与 handler lifecycle 由根 `network/internal/inbound` 验收：取消在途、等待完成、丢弃排队、FIFO、错误/panic/发送满关闭均通过。既有 TCP 慢 writer deadline、TCP/WS 发送队列满和 CloseWithProto 回归包含在受影响包全测中；没有把该结果称为真实慢连接容量试验。
- 新增成本：构造/认证/停止 benchmark 三次为 1084～1091 ns/op、561 B/op、8 allocs/op；4,096 个并存空队列调度器的本机 heap/stack 增量约 1294.4/8192 B 每连接，默认 8 帧数据积压理论约 32 KiB。原始命令、探针和边界见 [I50 调度成本](./performance.md#i50-dispatch-cost)。这项成本须计入 I41，不推断其他平台或真实 RSS。
- 所有修改 Go 文件均按 `.golangci.yml` 格式化。最终 `make check`、`make lint`、`go test -race ./network ./network/internal/inbound ./network/tcp ./network/websocket ./gateway -count=1 -timeout=180s` 全部通过；两个 module lint 为 0 issues，无新增或存量告警。新增关闭屏障测试后重跑最终检查，日志为 `i50-final-check.log`、`i50-final-lint.log`、`i50-final-race.log`；初轮结果不冒充最终结果。
- check 在当前进程移除 Redis/etcd 集成地址，真实 Redis/etcd、完整游戏均跳过。测试只使用本机临时 TCP/WS/gRPC、miniredis 和可控协议/生命周期屏障，cleanup 回收；未创建 VM 资源。未改协议、服务入口或构建链，未触发 breaking/build；I46 的预算不并入本提交。
- 完整 diff 与注册、认证、Forward、续租、Close/Kick、middleware 和发送队列调用链已复审；既有 handler 签名、协议、业务 FIFO 及配置拼写保持，仅新增显式能力和 RequestQueueSize。独立提交 `fix(network): 隔离业务处理与连接心跳`，不执行 push。

- I46 实施基线 `1bacab6`。`gateway/timeout_test.go`、`node/timeout_test.go` 观察依赖实际收到的 context：RPC/PushTimeout 设为 1ns 时，Gateway 清理/续租/backend 创建与 Node 两种回滚的 deadline 也被压缩。最终探针 `go test ./gateway ./node -run '^Test(RPCTimeoutDoesNotCancel|PushTimeoutDoesNotCancel)' -count=3 -timeout=30s` 五组各失败 3/3；日志 `i46-budget-repro.log`。初版只立即检查 ctx.Err，仅稳定发现一组，不能把该版其余通过当作无问题证据。
- 现有请求链保持逐层上限与较短父 deadline 优先，不改外部协议、Ludo/Whot 已开始任务的完成语义。Gateway 增加默认均为 3s 的 ConnectTimeout（依赖核验/共享 backend 创建）、LeaseTimeout（Gate 续租）、CleanupTimeout（独立清理/回滚/单 Session 排空）；Node CleanupTimeout 仅控制启动失败回滚，正常 Stop 仍由调用方预算控制。
- 定向通过：分离后的五组预算回归及 Node 回滚上限 `-count=20`；新预算覆盖、父 deadline、正常 Stop、Kick/排空等 `-count=10` 和 `-count=5`；`TestForwardBudgetUsesShortestDeadlineAcrossGRPC -count=3` 验证 transport、Forward、Node 或父 context 分别最短时，真实 gRPC 收到的预算和响应 Proto.Code 均正确。
- 修改 Go 文件已按 `.golangci.yml` 格式化；最终 `make check`、`make lint`、`go test -race ./gateway ./node -count=1 -timeout=180s` 全部通过，根/test module lint 为 0 issues，无新增/存量告警。日志 `i46-check.log`、`i46-lint.log`、`i46-race.log` 位于上述 B3 临时目录；旧测试中用 RPC/Push 参数控制清理/续租的夹具已按真实职责更新，未修改业务策略。
- test module 实际运行 `go test -race ./internal/mailbox ./ludo/internal/biz ./whot/internal/biz -run '^Test(CallsCancelWorkBeforeItStarts|ExecutorCallWaitsForStartedJobAfterCancellation|LoginWaitsForEntryAndKeepsCleanupBudget|LoginTimeoutKeepsCleanupBudget|DisconnectOverlappingReconnectKeepsNewSessionOnline|ReconnectAndTableCommandsUseCurrentSession|StaleDisconnectDoesNotOfflineReconnectedPlayer)$' -count=3 -timeout=60s`，三个包均通过。它只证明相应取消、清理和重连功能，不证明完整游戏或负载 SLO。
- check 未注入 Redis/etcd 集成地址；该类集成及完整游戏按入口条件跳过。无入口/构建链/协议改动，未触发 build/breaking。生产改动限于预算所属字段与对应调用，完整 diff、初始化/建连/Forward/续租/Kick/排空/epoch 回滚调用链已复审；正常 Stop 和请求错误身份保持原有路径。独立提交 `fix(runtime): 分离请求与生命周期超时预算`，I46 保留待验证，不将框架修复等同于业务容量通过。

<a id="b5-results"></a>
## B5 验证记录

- I53 基线 `7c12592`，起始工作树干净。先增加只读查询 API 和测试，在原 dispatch 尚未注入 metadata 时运行 `go test ./node -run '^TestCommandMetadataDistinguishesSharedRequestsThroughGRPC$' -count=3 -timeout=30s`，存在性断言失败 3/3；日志 `%TEMP%\yola-b5-20260925-7c12592\i53-repro.log`。
- 在 dispatch 查找 handler 后增加一次不可变 context 注入；`CommandFromContext` 返回实际 int32 command 与存在标记，没有 setter、状态字段或第二套路由表。保留 Session 所有权和 Kratos operation，RawHandler 只获得 context 元数据，不自动应用 typed middleware。
- `golangci-lint fmt --config .golangci.yml node/command.go node/command_test.go node/dispatch.go`；`go test ./node -run '^TestCommandMetadata' -count=20 -timeout=60s`；`make check`、`make lint`、`go test -race ./node -count=1 -timeout=120s` 均通过。两个 module lint 为 0 issues，无新增/存量告警；日志 `i53-check.log`、`i53-lint.log`、`i53-race.log` 位于同目录。
- 真实本机 gRPC 覆盖共享 Empty 请求的 command 11/22、middleware 前后顺序、Session UID、原始 handler error、固定 operation、RawHandler command 0；普通/nil context 返回缺失。check 未注入 Redis/etcd 地址，外部集成/完整游戏的跳过不算通过；无协议、入口、依赖或构建链改动，未触发 build/breaking。测试资源由 cleanup 回收。
- 全部任务 diff、新增文件及 Forward → dispatch → typed/RawHandler 的调用链已复审；独立提交 `feat(node): 提供请求级业务 command 元数据`，不执行 push。

- I54 基线 `731bd1f`，工作树原先干净。通过 `%TEMP%\yola-b5-20260925-7c12592\i54-overlay.json` 加载两个包的临时 `ownership_probe_test.go`，执行 `go test -overlay <该文件> ./network/tcp ./network/websocket -run '^TestAuditSendProtoReturnDoesNotPromiseSnapshot$' -count=3 -timeout=30s`：TCP 快照假设失败 3/3，WS 对照通过；日志 `i54-probe.log`。探针没有并发写入，仅证明实现差异，不视为受支持的复用契约。
- 调用链审计：Gateway RPC Push 为新 Proto 且之后不改写入站 Body；Broadcast 先复制 Payload，fanout 后只复用 Prepared 视图；transport 每帧创建独立解码对象，发送后不再改写；客户端 Request 先 marshal 业务参数并新建请求 Proto；benchmark 共享对象保持只读。未发现需要修正的仓库内生产调用方，外部 handler/codec 仍须遵守契约。
- 选择不可变输入，不改变 TCP/WS 编码位置、buffer、错误时机或分配策略。新真实连接测试覆盖四种组合（TCP/WS × 默认/自定义 codec）、16 个并发发送者共享输入、clone 后修改独立数据，以及 Prepared.Reset 后旧消息可继续发送。`go test ./network -run '^TestSendProtoSharesImmutableInputAcrossTransports$' -count=20 -timeout=60s` 及同筛选 `-race -count=20` 均通过。客户端 PushHandler 提供的是业务 Payload，验收按该接口比较数据；初稿夹具误作完整 Proto 解码，已修正。
- `go test -race ./network ./network/tcp ./network/websocket -count=1 -timeout=120s`、`go test -race ./gateway -run '^TestBroadcast' -count=3 -timeout=60s` 通过。初轮 lint 指出新增测试的分支可改为 switch，已修正；最终 `make check` 和 `make lint` 均通过，两个 module 0 issues，无存量告警。日志为 `i54-race.log`、`i54-final-check.log`、`i54-final-lint.log`；修改 Go 文件均已格式化。
- 复审覆盖 SendProto/内部 push/reply、Prepared fallback、Gateway Push/Broadcast、客户端 Request、测试及 benchmark 的所有调用点。库内未发现发送成功后改写的生产路径；没有将文档约束当作违规写入被自动修复。无新生产分配或性能收益声明，无协议、入口或依赖变更，未触发 build/breaking；check 的外部 Redis/etcd/完整游戏跳过仍不算通过。独立提交 `docs(network): 统一 SendProto 不可变消息契约`。

<a id="i55-baseline"></a>
### I55 调查与基准（实施前）

- 基线 `2cc1743`，基准运行前工作树干净；本轮按用户确认停在交接，不新增观测接口或修改运行逻辑。以下是后续设计输入，不是已冻结方案或修复通过的证据。
- [NATS 订阅](../event/nats/subscription.go#L41) 拥有有界消息 channel；现有超限计数私有，原生 Dropped 主要在退订前读取。固定 nats.go v1.53.1 在关闭后调用 Delivered/Dropped 返回 ErrBadSubscription；ChanSubscribe 在退订的 removeSub 路径中于订阅锁内执行关闭回调，不能在回调里重入这些方法（依赖源码 nats.go:5102、5810、5826）。后续须区分实时读数、最后可得快照和关闭竞争下未知的增量，不能承诺原生最终 drop 精确值或由此推断 broker 上游丢失。
- TCP/WS 各自拥有发送队列，Gateway 已有 BroadcastStats。先明确每层计数及字节口径、累计值的生命周期和读取并发，再选择最小只读扩展；复用现有 owner，不为观测新增通用 manager、第二套连接注册表或消息队列。不得把逻辑排队字节当作 RSS，或把写出成功当作客户端已收到。
- 根目录实际运行：`go test ./event/nats -run '^$' -bench '^BenchmarkDispatch$' -benchmem -benchtime=1s -count=3`，三次为 11.44/10.96/10.69 ns/op，均 0 B/op、0 allocs/op；`go test ./network/tcp -run '^$' -bench '^BenchmarkTCPSlowConsumerBackpressure$' -benchmem -benchtime=1s -count=3`，三次为 2906/2733/2650 ns/op，均 1136 B/op、7 allocs/op。
- 环境：Go 1.26.6、windows/amd64、Intel i7-9700K；两条命令均只运行指定 benchmark，不运行功能测试，不需要外部 broker/Redis/etcd。前者直接调用空 handler，后者仅填充本地发送队列，均不代表端到端吞吐或慢 socket 验收。原始日志为 `%TEMP%\yola-b5-20260925-7c12592\i55-dispatch-before.log`、`i55-send-before.log`；临时日志可能被系统清理，后续对比前须确认代码、工具、配置和环境一致。
- 当时下一步为分层屏障验收与最小实现；该基准本身不是修复后验证。后续实施及当前结果见下节。

<a id="i55-results"></a>
### ~~I55 在线容量观测~~

- 基线为干净 `346cfaa`；先只增加观测类型与能力断言，NATS/TCP/WS 原实现缺少只读能力各失败 3/3（`red.log`），不将该断言写成已运行真实容量验收。实现只修改原有 owner，Gateway 生产路径未变；新接口均为可选能力，不扩展原 Subscription/Connection 必需方法。
- NATS 从原 bounded channel 读 depth，锁保护原生 drop 最后快照及 handler 累计/最近/最大耗时，沿用超限原子计数。关闭释放 channel/handler 引用，原生读取失效时 Current=false；不从原生关闭回调重入锁，不承诺最终 drop、上游丢失或关闭丢弃计数。TCP/WS 用原子计数记录待入队和排队的逻辑 Body 字节及队列满拒绝，取消回滚，writer 取走即扣除；关闭后的残留队列仍可观测，不代表 RSS 或客户端送达。
- 真实内嵌 NATS v2.14.5 的阻塞 handler 验收固定 2 条积压、3 次原生拒绝，并验证超限仅在出队时计数、panic、正常关闭及原生先失效的快照保留。TCP net.Pipe、WS 真实 framing 加受控 writer 验证慢发送、不同 Payload、取消回复/最后帧回滚。Gateway 屏障分别验证广播队列拒绝与连接拒绝，保留现有 BroadcastStats 累计值；同一次连接拒绝不能跨层相加。
- 根目录命令：`go test ./event/nats ./network/tcp ./network/websocket ./gateway -run '^(TestSubscriptionStats|TestSendStats|TestBroadcastQueueIsBounded|TestBroadcastStats)' -count=20 -timeout=60s` 通过；最终增强并发关闭交错后同筛选 `-race -count=20` 通过。`go test -race ./event/nats ./network ./network/tcp ./network/websocket ./gateway -count=1 -timeout=120s` 通过；最终 `make check`、`make lint` 通过，两个 module 0 issues，无存量告警。所有修改 Go 文件已格式化。首版夹具未超过 Windows 时钟分辨率，耗时断言偶发为 0；增加单独计时窗口后通过，拥塞时序仍用屏障。
- 环境为 Go 1.26.6、Windows/amd64、i7-9700K、golangci-lint 2.13.2；race 在命令内前置 MSYS2 GCC 路径。日志保存于 `%TEMP%\yola-i55-20260925-346cfaa`：`stats-tests.log`、`race.log`、`final-stats-race.log`、`final-check.log`、`final-lint.log`。check 中外部 Redis/etcd/完整游戏按未设置环境变量跳过，不计为通过；无协议、入口或依赖改动，不触发 build/breaking。
- 三组基准顺序执行且无并行检查；具体命令及成本见 [I55 性能记录](./performance.md#i55-observation-cost)。NATS dispatch 中位数 10.62→38.91 ns/op，保持 0 B/0 alloc；TCP 32 帧入队再拒绝一次 2582→2793 ns/op，1136→1152 B/op、均 7 alloc；WS 4KB 往返 87.734→88.954µs、均 20 alloc，不能把单机波动当吞吐承诺。
- 完整 diff 已复审，覆盖 NATS 注册/消费/退订、TCP push/reply/final/next、WS 普通/Prepared/heartbeat/final/writeLoop，以及 Gateway 发送失败传播；没有引入第二套注册表、manager、消息队列或修改 Kratos/Registry。I55 按已验证的观测边界关闭，I41/I44 的真实负载、RSS 与业务 SLO 仍待 B6。

<a id="i48-results"></a>
### I48 实施与验证（基线 70e1777）

- **实现**：`NodeLocator.BindNode/UnbindNode` 增加 epoch 参数，Node 使用自己的身份传入，业务 Session API 不变。Node binding/epoch 使用 RawURL 编码的 service hash tag；单机与 Cluster 共用 Lua，先校验 epoch，再覆盖或按 NodeID 删除。确定失权会先取消现有 lease，再走既有生命周期失败入口；普通依赖错误不撤销本代。保留有效进程 LWW、NodeID 值、6h TTL、同 ID 重启继承、Gate key 和唯一 Registry，无旧布局 fallback、额外队列或重复状态。
- **红色基线**：在 `70e1777` 的独立源码上运行旧四参数 API 探针，miniredis 与专用 Redis 8.6.1 中，迟到 Bind 覆盖新 Node、迟到 Unbind 删除同 ID 新代均各失败 3/3；不同 NodeID Unbind 对照通过。对应 `baseline-red.log`、`standalone-red.log`，没有把失败命令计为通过。
- **回归边界**：真实 socket Write 屏障先完成本地准入，再交接存储 epoch、写入新绑定、取消旧调用并释放 Write；检查底层确实返回 epoch conflict，排除仅凭取消得到假阳性。覆盖同/不同 NodeID、missing/expired epoch、LWW、拒绝时 TTL 不刷新、迟到续租不复活、首因保留和 Drain 失权。原有 Gateway、Registry 及 I49 排空路径一起验收。

| 执行位置 / 命令 | 实际结果与日志 |
| --- | --- |
| 根目录：`make check`、`make lint` | 最后一次 Go 修改后通过；两个 module lint 均 0，新增/存量告警均 0。`final-check.log`、`final-lint.log`；check 中未配置的外部测试跳过，真依赖结果见下列独立命令 |
| 根目录：`go test -race ./locate/... ./node ./gateway -count=1 -timeout=180s` | 通过，`package-race.log`；随后仅补 Node 外部夹具 password 注入，重跑 `go test -race ./node -count=1` 通过，`final-node-race.log` |
| 根目录：`go test -race ./node -run '^Test(Binding.*\|LateBindingWritePreservesReplacement\|StopWaitsForSavedSessionBinding\|SessionBindingDrainTimeoutKeepsEpoch\|BusinessDrainCanBindAndUnbindSavedSession)$' -count=20 -timeout=120s` | Windows 定向 20 轮通过，`node-targeted-race.log`；正则分支字符使用 Markdown 渲染后的命令 |
| VM：`./node-race.test -test.run '^Test(Binding.*\|LateBindingWritePreservesReplacement\|AppPreparedNodeCannotOverwriteReplacement)$' -test.count=3 -test.timeout=180s -test.v` | 单机/Cluster 迟到写、生命周期与 Redis/etcd 注册回归 3 轮通过，最终源码 `final-node-integration-race.log` |
| VM：`./gateway-race.test -test.run '^TestGatewayNodeIntegration$' -test.count=3 -test.timeout=180s -test.v` | 真实 Redis/etcd 链路 3 轮通过，`gateway-integration-race.log`；此后 Gateway 源码未变 |
| VM：`./locator-race.test -test.run '^TestNodeBindingRedisIntegration$' -test.count=3 -test.timeout=120s -test.v` | 单机/Cluster 相同存储契约 3 轮通过，`final-locator-integration-race.log` |
| VM：分别运行 `./locator-race.test -test.run '^TestNodeBindingClusterMigration$' -test.count=1 -test.timeout=90s -test.v`、`./locator-race.test -test.run '^TestNodeBindingClusterCooperativeFailover$' -test.count=1 -test.timeout=90s -test.v` | 每场景各用 3 个独立新建且 6 节点就绪的集群，6 个 race 样本通过，`isolated-race-Migration-{1,2,3}.log`、`isolated-race-CooperativeFailover-{1,2,3}.log` |

- **Cluster 验收范围**：3 主 3 从，Migration 覆盖部分 key 迁移时 TRYAGAIN、不破坏 binding、ASK/MOVED 恢复及旧 epoch 拒绝；合作式 Failover 用同连接 SET/WAIT、复制代次/offset 和 6 节点视图收敛确认前置状态，再验新 owner 拒绝旧代。它不模拟异步丢写或网络分区，不证明 Redis 故障切换没有数据回退。
- **未通过的组合与夹具修正**：首次夹具用 STABLE/NODE 恢复 slot，未正确提高 configEpoch；后续切主触发 Redis 8.6.1 `server.c:3600` 断言退出（exit 139，OOM=false）。已改为反向 IMPORTING/MIGRATING/NODE 和全节点视图收敛，保存 `redis-crash-initial.log`、`redis-crash-state.log`，未改 Redis 源码或版本。修正后连续组合无 race 3 轮通过（`cluster-transitions-final.log`），但连续组合 race 的第二轮合作式切主仍 timeout（`locator-integration-race.log`），该命令失败；复制重同步日志保留，不能把独立新环境通过改写为快速连续管理操作稳定。启动前 loading/连接拒绝的样本也保留，不计成功。未发现旧代通过 Lua 后破坏已交接 binding 的反例，因此按 I48 存储修改权范围关闭，保留上述测试限制。
- **环境与清理**：Go 1.26.6、golangci-lint 2.13.2；Linux race 使用 VM 已安装 GCC 11.5.0。专用 Redis 8.6.1 单机与 6 节点 Cluster、etcd 3.5.21，仅回环端口 16400/16410～16415/12400，全部带 `yola.task=i48-70e1777` label 和 CPU/内存/tmpfs 限额。单机 DB9、Cluster DB0 与 etcd 任务 prefix 最后为空。归档下载核验后，按完整 ID/名称/label/挂载复核并删除 8 个任务容器；验证任务目录 realpath 后删除 `/tmp/yola-i48-70e1777`。`cleanup.log` 确认无任务容器/目录残留，既有服务未操作。
- **证据与复审**：本机 `%TEMP%\yola-i48-70e1777` 保存本地与 VM 日志；`evidence.tar.gz` 包含最终源码、manifest 和原始失败/成功日志，SHA-256 为 `be5e4efe7d4ea18936792f180fcaa1ce2029390aac96e629f7ad6f90cc984511`。临时产物不保证长期保留。完整 diff 含新增测试及所有 Locator 调用方已复审，修正文档旧 slot/重试口径；最后 Go 检查后仅改文档，不重复检查。没有协议/入口改动，未运行 breaking/build。I03/I36 保持未关闭。
- **交付检查**：根目录 PowerShell 核对 187 个本地链接/锚点/行号、两份文档 20 个 ID、10 个关闭标记和 3 个完成子项一致；最终核心源码与外部验收 manifest 一致。工作树和暂存区 diff 检查通过，仅提交复审过的 I48 改动及交接记录。

### 待恢复的 Redis deadline 诊断

此项仅保存已获得的证据，本轮不实施：go-redis v9.22.0 默认 `ContextTimeoutEnabled=false`，已预热的真实 TCP 连接在 caller 100ms deadline 后仍等到约 301ms 才返回成功；独立探针 3/3 失败，pool wait/dial 增量为零。启用后约 100ms 返回 `net.OpError` timeout，但不匹配 `context.DeadlineExceeded`，现有错误映射还需修正。单纯 cancel 无 deadline 时两配置都可能完成已开始读写，不应新增回滚承诺。证据位于 `%TEMP%\yola-redis-deadline-eb182b0-20260926` 的 README、overlay 与日志；该红色 race 命令 exit 1，不能计为通过。后续从 client 创建方与 Locator 网络错误边界修复，不修改注入的共享 client、不覆盖成功结果或 I48 fencing 错误。

## 验证环境边界

### I48 历史调查环境与结果（实施前）

- 代码基线 `0883c00`，未修改生产代码或正常测试集。Overlay 探针及 miniredis、Redis 的命令和结果见 [设计调查](./node-binding-fencing.md#迟到写入证据)。真实 Redis 下 `-race` 三轮仍为功能失败，未报告 data race；I48 未修复，未关闭。顺带修正架构文档 §3.3 对启动失败预算的残留名称为已实现的 CleanupTimeout，不重做 I46。
- VM 只读核验为 4 vCPU、约 7.5GiB 内存，已有 Redis/etcd/NATS/MySQL/Consul 均未操作。任务容器 `yola-i48-20260925-redis` 与 `yola-i48-20260925-cluster` 各限 0.5 CPU/128MiB、64MiB tmpfs；前者仅 VM 回环 16379 并用 SSH 转发，DB 9、随机 service，后者无网络且无暴露端口。镜像实际 Redis 为 8.6.1。
- 验证结束后重新核对两容器完整 ID、名称、任务 label 和 tmpfs，仅删除本轮两容器；按 PID、ssh.exe 名称及转发参数核实并停止本轮隧道。任务 label 查询已无残留容器，未操作既有服务。
- 单节点 Cluster 的全部 slots 只用于无业务数据的原子原语试验：旧布局两 key 返回 CROSSSLOT；候选同 slot 的旧 epoch Bind/Unbind 均拒绝，当前 epoch Unbind 成功。未运行多节点 failover、完整 Kratos 重启或自然 TTL 交错，不用原型证明方案已经实施。
- 产物位于 `%TEMP%\yola-i48-20260925`：`probe_test.go`、`overlay.json`、`miniredis-red.log`、`redis-red.log`、`redis-race-red.log`、`cluster-setup.log`、`cluster-proof.log`。本轮 tracked 改动仅为文档，核对引用、命令、约束及完整 diff；不重跑无变化的全库检查，不将 I55 的历史通过写成 I48 通过。

### 外部环境约束

- 工作目录为 `D:\src\pitaya\yola`，本地 shell 为 PowerShell；根 module 与 `test` module 各自执行其适用命令。
- 可免密 SSH 到 `192.168.152.129` 使用 Docker；先只读确认主机、已有容器、端口、挂载和数据范围，不能假定历史容器列表仍有效。
- 仅使用本任务专用实例、专用 DB/prefix、可丢弃 UID；Redis/etcd 地址通过 `YOLA_REDIS_INTEGRATION`、`YOLA_ETCD_INTEGRATION` 注入，不修改 tracked 配置。NATS 同样隔离 broker/Topic 与负载。
- 清理前核对资源名称、ID、归属与实际路径，仅删除本任务创建的对象；不得重启、清空或删除无关服务和数据。凭据不进入源码、命令输出或交接文档。

## 新会话提示词

清理窗口后可复制以下内容；提示词以文档和当前工作树定位进度，不假定此前会话状态仍然有效。

```text
请继续 D:\src\pitaya\yola 的架构深度审核与分步修复，重点是根 module；test 仅作为框架扩展与必要验收案例。

1. 先检查 git status --short、暂存/未暂存 diff、新增文件和当前 HEAD；保留已有改动。读取适用 AGENTS.md/AGENTS.override.md、docs/README.md、docs/issues.md、docs/refactor-progress.md，并按当前条目阅读 architecture.md、eventbus.md、performance.md 和相关代码。
2. 阅读已安装且与任务相关的 skills；整体架构评估使用 improve-codebase-architecture、codebase-design、code-review，复现与修复使用 diagnosing-bugs，并发/生命周期使用 golang-concurrency，检查使用 golang-lint，符号追踪按需使用 golang-gopls。不机械叠加流程或新增审批。
3. 以 Git 和进度文档恢复。I55 提交 0883c00，组件边界 cf85f6e，I46/I45 共用验收入口 b1976ed，根框架取消/排空验收 eb182b0，I44 提交 70e1777，其后完成 I48，实际 HEAD 以 Git 为准。I44、I47、I48、I49、I50、I51、I52、I53、I54、I55 已关闭，保留划线，不重做。I46/I45 已完成子项同步划线，父 issue 未关闭。上轮按用户要求提交后停止；本提示词仅用于用户再次要求继续时恢复。项目未上线、仅本地 Docker，I07 不恢复。
4. 用户明确外层原生 Kratos App 是唯一应用入口，Gate/Node 是内嵌组件；不新增 App/Config、运行器或隐藏覆盖语义的组合入口。沿调用链理解根因和职责后再实施，不按 issue 编号机械修复。当前没有容量 SLO，不阻塞独立架构诊断，也不自行宣布容量通过。
5. 固定 Kratos v3.0.0，不修改官方源码或升级；保留唯一共享 Registry 与条件注册。I48 已按 A 实施：Node binding/epoch 按 service 同 slot，单机与 Cluster 共用 Lua，不保留旧格式兼容，保持 LWW 与原 ID 重启继承；连续快速管理操作组合 race 未通过，保留其限制，I03/I36 不由此关闭。后续先修已复现的 I46 Redis deadline 与错误分类，再推进 I04/I34 成本、I41 和完整游戏窗口/同配置负载。其他重大行为/存储变化仍先准备方案与证据并确认。
6. 按 AGENTS 的风险要求完成实际测试、lint、race、check、build 或 breaking；先检查 TestMain/环境依赖。可免密 SSH 到 192.168.152.129 用 Docker，但操作前确认资源，使用专用可丢弃实例，避免影响无关服务与数据。
7. 每完成一个原子步骤，同步两份文档的阶段、证据、命令结果、未完成项、代码基线和下一步；已关闭问题保留原条目，在两份文档同步划线。不要把静态推导、候选方案或旧测试写成当前已验证；遇到代码事实推翻假设时修正文档。
8. 在已授权范围内持续推进，不只停留在计划；每步简述结果。每个问题完成复现、契约核对、最小修复、必要验证、完整 diff 复审和文档后独立 git commit，格式 <type>(<scope>): <中文摘要>，由用户最后统一审核；不要求逐项 /plan 或 /clear。不授权 push 或发布。
```
