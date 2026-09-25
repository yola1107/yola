# 架构审查与修复进度

本文件是后续会话的执行交接入口，只维护阶段、验证证据和下一步；问题事实、候选方案和关闭条件见 [issues.md](./issues.md)。
恢复工作时以当前 Git、代码及本表为准；历史对话、静态审查和候选方案均不证明修复已经完成。

## 当前快照

- **更新日期**：2026-09-25。
- **代码审查基线**：起点 `0c8b270`；初始化交接文档 `9378b54`，I49 修复 `1a882f6`，I47 修复 `eb44302`，I52 随本提交关闭。提交边界按问题划分。
- **当前验证**：I52 定向 20 轮、`go test ./event/nats`、该包 race、`make lint` 通过；根/test module lint 均为 0 告警。本轮未重跑 B1 的 check、构建和 Redis/etcd 验证，其原始结果保留在下方记录。
- **工作重点**：根 module 的架构与契约；`test` 是扩展与验收，不再以游戏局部重构替代框架分析。
- **Git 边界**：用户已授权按问题独立提交；仅提交复审过的任务改动。交接文档初始化、I49、I47 分别提交；未授权 push 或发布。
- **当前范围**：B1 已完成，B2 按 I52 → I51 推进。固定 Kratos v3.0.0，不修改官方源码；保留唯一共享 Registry 与已确认的条件注册语义，不重做已关闭问题。
- **下一步**：I52 已闭环，继续 I51 的重复订阅 ACL、重连和异步错误交错，核实固定 nats.go 的订阅级能力；若契约无法满足，准备证据及具体方案后确认。I48 仅设计调查，不迁移绑定格式。

## 状态口径

- `待复现`：已有静态证据，故障交错尚未运行验证。
- `待设计`：需明确边界或比较方案，不能把候选方案当作实现命令。
- `待实现`：方案与必要决策已明确，代码尚未完成；`实施中` 表示已开始修改。
- `待验证`：实现或基线已有，但关闭条件尚未满足；`约束` 表示继续遵守现有限制或由部署所有者验收。
- `已关闭`：修复、必要测试、完整 diff 复审均完成，记录提交/工作树证据与剩余限制；只完成文档不关闭问题。

## 问题进度表

| 问题 | 当前阶段 | 已有证据与验证边界 | 下一步 / 关闭记录 |
| --- | --- | --- | --- |
| ~~[I47 就绪与注册](./issues.md#i47)~~ | 已关闭（eb44302） | 原问题各复现 3/3；就绪适配、空键事务和本代 lease 注销已通过真依赖/race；完整 diff 已复审 | 唯一共享 Registry，官方 Discovery/Session；同 ID 旧记录未回收则冲突，lease 丢失后需重建应用 |
| ~~[I49 Session 副作用排空](./issues.md#i49)~~ | 已关闭（1a882f6） | 保存 Session 的后台 Bind/Unbind 已接入现有 deliveries；定向、包测试、race、lint 通过并复审 | Drain 内可操作；停止后拒绝；超时/重复 Stop 不释放 epoch；不解决 I48 |
| [I48 binding 代次保护](./issues.md#i48) | 待设计 | 已核对同 ID 重启、覆盖绑定及不同 Redis slot；未运行迟到写入交错 | 比较同 service slot 的原子核验与保留 UID 分片的协调成本；不直接改格式/API |
| [I51 NATS 激活归属](./issues.md#i51) | 待复现 | LastError 覆盖与文本比较已静态核查；未运行错误交错 | B2：重复 ACL 和异步错误交错，验证底层能力后设计 |
| ~~[I52 等待注册取消](./issues.md#i52)~~ | 已关闭（本提交） | 原实现失败 3/3；屏障回归 20 轮、包测试/race、lint 通过 | 获锁后重查 caller context；未发 SUB，第三次成功；激活后终态及 Close 语义不变 |
| [I50 心跳调度](./issues.md#i50) | 待复现 | 两种 transport 串行读取/处理；未运行误断线场景 | B3：真实慢请求、pipeline、发送拥塞，比较有界方案 |
| [I46 预算所有者](./issues.md#i46) | 待设计 | 已有多层上限与配置差异；新增框架回滚预算耦合分析 | B3：分清请求/租约/清理，保持独立清理和已开始操作语义 |
| [I53 command 元数据](./issues.md#i53) | 待设计 | dispatch 只注入 Session；未运行 middleware 区分验收 | B5：定义最小元数据契约并核对 operation 调用方 |
| [I54 消息所有权](./issues.md#i54) | 待设计 | TCP 保留指针、WS 先编码；未运行复用/race 场景 | B5：核对所有调用方，选择不可变契约或隔离编码方案 |
| [I55 在线容量观测](./issues.md#i55) | 待设计 | 当前统计边界已核查；未完成分层观测 | B5：按 owner 补指标，为 B6 提供证据 |
| [I36 Gate 强单活](./issues.md#i36) | 待设计 | 现有 best-effort Kick 的已知限制 | 独立决策：确认是否要求强单活及已开始操作边界 |
| [I07 历史凭据与安全](./issues.md#i07) | 约束 | 历史暴露记录；轮换未确认，本轮无生产核验 | 部署所有者提供去敏轮换与安全验收证据 |
| [I40 NATS 生产安全](./issues.md#i40) | 约束 | 2026-08-20 开发环境观测，非当前环境结论 | 获准后核查目标 broker，验收认证、mTLS 与 ACL |
| [I03 binding TTL](./issues.md#i03) | 约束 | 固定 6h 与显式刷新契约 | 验证长业务刷新；若新增能力，与 I48 联合设计 |
| [I08 sticky 切换](./issues.md#i08) | 约束 | 首次模式固定，后续变化 fail closed | 保持重启约定；有在线迁移需求后再设计 |
| [I29 可信代理 IP](./issues.md#i29) | 约束 | 当前使用 socket peer | 代理需求明确后设计并验收信任边界 |
| [I41 真实连接容量](./issues.md#i41) | 待验证 | 只有部分进程内分配收益，五档真实容量未关闭 | B6：I55 就绪后执行单 Gateway 五档验收 |
| [I44 NATS 排队内存](./issues.md#i44) | 待验证 | 已有理论上限与局部 heap 试验，非完整 RSS 边界 | B6：按真实 broker 上限验证 backlog、drop、RSS、p99 |
| [I04 三次 Redis 查询](./issues.md#i04) | 约束 | 热路径成本明确，远程查询未减少 | B6：测量后决定是否优化，不删除 fencing；依赖 I47/I48 |
| [I34 续租波次](./issues.md#i34) | 约束 | 无跨 Session 整形，真实故障容量未验收 | B6：区分调度阻塞与 Redis 拥塞，量化后再决定 jitter |
| [I45 Table Push 长尾](./issues.md#i45) | 待验证 | 已有突发入座与局部分段数据，稳态/完整对局未关闭 | B6 后续扩展验收；保持桌内顺序，不作为框架主线 |

## 建议批次与实施边界

以下是待实施顺序，不代表已获准改变现有行为；遇到实质性语义决策，仅暂停依赖该决策的动作，继续其他已授权工作。

| 批次 | 目标与收益 | 风险 / 依赖 | 验证出口 |
| --- | --- | --- | --- |
| B1 | I47、I49：实例就绪发布与框架副作用排空 | 中；I48 同步调查但不迁移存储。就绪屏障不能冒充完整跨存储原子性 | App 生命周期交错、排空/超时/race；需要时用隔离 Redis/etcd |
| B2 | I51、I52：准确报告订阅激活，消除无谓终态 | I52 低，I51 中；保留已明确的 EventBus 投递与失败契约 | 重复 ACL、异步错误交错、等待取消、Close 与 race |
| B3 | I50、I46：连接存活与请求/清理预算职责 | 中高；控制帧调度、deadline、已开始操作语义需明确 | 真实 TCP/WS/gRPC、慢请求、拥塞、顺序、停止、同配置负载 |
| B4 | I48：旧进程不能破坏新 binding | 高；须确认存储格式、原 ID 重启及条件更新方案，结合 I47 | 迟到 Bind/Unbind、新旧代交错、真实存储与 Cluster 约束 |
| B5 | I53、I54、I55：补齐扩展接口与观测 | 低至中；按独立职责拆分原子改动，不建通用管理层 | command 区分、消息所有权/race、分层统计及热路径成本 |
| B6 | I41、I44、I04、I34，再按需 I45：容量与优化验收 | 先有观测及固定预算；生产部署约束另行处理 | 固定代码/配置/资源，记录 p99、drop、RSS 与资源归还 |

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

前序 `470a4fa`、`7959b58`、`a83ccb2`、`0c8b270` 已提交 mailbox/Whot 局部修复；它们不关闭本表新增架构问题。历史测试或性能文档不能直接证明后续代码通过，复用结果须核对源码、依赖、配置和环境均未变化。

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

- I52 接手基线 `eb44302`，工作树、暂存区和新增文件均为空；本问题独立提交 `fix(event): 避免等待注册取消污染订阅能力`。生产改动只在 `event/nats/event.go` 的注册锁内、底层激活前重查 caller context，保留 Bus Close 和历史终态错误优先级。
- 原实现运行 `go test ./event/nats -run '^TestSubscribeCanceledWhileWaitingDoesNotActivate$' -count=3 -timeout=30s`，取消污染断言失败 3/3。`activation_test.go` 通过真实 nats.go 连接和本机协议端扣住 PONG，让第一注册持锁，再取消已通过初检的第二注册；修复后连续 SID 和线上命令证明未创建第二个底层订阅。
- 格式化：`golangci-lint fmt --config .golangci.yml event/nats/event.go event/nats/activation_test.go`；定向：`go test ./event/nats -run '^Test(SubscribeCanceledWhileWaitingDoesNotActivate|SubscribeCancellationAfterActivationRemainsTerminal|CloseRejectsSubscriptionWaitingForRegistration)$' -count=20 -timeout=60s`，全部通过。
- `go test ./event/nats -count=1 -timeout=120s`、`go test -race ./event/nats -count=1 -timeout=120s`、`make lint` 全部通过；根/test module lint 均为 0 issues，无新增或存量告警。工具为 Go 1.26.6 windows/amd64、golangci-lint 2.13.2；race 仅在该命令前置 `D:\soft\msys64\mingw64\bin` 到 PATH。
- 测试只使用本机回环随机端口的协议夹具及依赖内嵌 NATS Server v2.14.5，不读外部测试地址，无 VM 资源；测试 cleanup 回收连接和服务器。I52 不改公开 API、依赖、入口、协议或跨包代码，未触发 make check、build、breaking。
- 限制：等待 mutex 的调用仍在获锁后返回取消；取消检查之后才发生的取消属于已进入激活的失败，仍终止注册能力。I51 的重复 ACL 和异步错误归属不由 I52 关闭，下一步独立复现并评估固定依赖。

## 验证环境边界

- 工作目录为 `D:\src\pitaya\yola`，本地 shell 为 PowerShell；根 module 与 `test` module 各自执行其适用命令。
- 可免密 SSH 到 `192.168.152.129` 使用 Docker；先只读确认主机、已有容器、端口、挂载和数据范围，不能假定历史容器列表仍有效。
- 仅使用本任务专用实例、专用 DB/prefix、可丢弃 UID；Redis/etcd 地址通过 `YOLA_REDIS_INTEGRATION`、`YOLA_ETCD_INTEGRATION` 注入，不修改 tracked 配置。NATS 同样隔离 broker/Topic 与负载。
- 清理前核对资源名称、ID、归属与实际路径，仅删除本任务创建的对象；不得重启、清空或删除无关服务和数据。凭据不进入源码、命令输出或交接文档。

## 新会话提示词

清理窗口后可复制以下内容；提示词以文档和当前工作树定位进度，不假定此前会话状态仍然有效。

```text
请继续 D:\src\pitaya\yola 的架构深度审核与分步修复，重点是根 module；test 仅作为框架扩展与必要验收案例。

1. 先检查 git status --short、暂存/未暂存 diff、新增文件和当前 HEAD；保留已有改动。读取适用 AGENTS.md/AGENTS.override.md、docs/README.md、docs/issues.md、docs/refactor-progress.md，并按当前条目阅读 architecture.md、eventbus.md、performance.md 和相关代码。
2. 阅读并使用已安装且相关的 skills（至少 codebase-design、code-review；并发、诊断、Go 导航和 lint 按实际任务选用），不要只依据本提示或历史结论改代码。
3. 以 refactor-progress.md 的当前阶段、证据和下一步恢复。初始化交接基线是 0c8b270，I47～I55 当时只有静态发现、未运行故障复现、未修复；若文档或 Git 已有更新，以新证据为准，不重做已完成项。
4. 从当前未完成批次推进；B1 与 B2 的 I52 已关闭，下一步为 I51 的重复 ACL、重连和异步错误归属。先稳定复现、核对契约、简要说明收益与风险，再实施最小修复并复审相关调用链。I48 先做设计调查，不能直接迁移绑定格式。
5. 保持现有业务行为、协议及对外接口，优先删除重复和收敛职责，避免 BaseServer、通用 manager 等无独立职责抽象。涉及存储模型、同 ID 重启、强单活、控制帧调度或 timeout 语义的重大变化，准备具体方案后先确认；只暂停相关部分。
6. 按 AGENTS 的风险要求完成实际测试、lint、race、check、build 或 breaking；先检查 TestMain/环境依赖。可免密 SSH 到 192.168.152.129 用 Docker，但操作前确认资源，使用专用可丢弃实例，避免影响无关服务与数据。
7. 每完成一个原子步骤，同步两份文档的阶段、证据、命令结果、未完成项、代码基线和下一步；已关闭问题保留原条目，在两份文档同步划线。不要把静态推导、候选方案或旧测试写成当前已验证；遇到代码事实推翻假设时修正文档。
8. 在已授权范围内持续推进，不只停留在计划；每步说明改动、验证和风险。每个已关闭问题单独提交，仅提交完整复审的任务改动；不授权 push 或发布。
```
