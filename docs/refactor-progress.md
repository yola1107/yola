# 框架清理进度

本文件区分本次实施、三轮审查和历史修复，当前状态见 [issues.md](./issues.md)。历史修复、排期和提交授权不自动适用于后续任务。

<a id="cleanup-results"></a>
## I56–I64 实施与验证（已完成）

- **日期与基线**：2026-09-26，`9ca9ad78d4dd8c0803017b5fcf692d90239e7ea4`。起始HEAD、status、暂存/未暂存diff及新增文件均已核对，工作树干净；第一轮清理为`ab0479b`、三轮审核归档为`a9a0cf6`。两者到本次基线的Go源码无变化。AGENTS.md未修改。
- **范围与状态**：九项P1全部完成代码/测试调整、验证和完整任务diff复审；按网络装配与fixture、PreparedProto缓存发布、NATS覆盖合并、Gateway/Node测试清理分批处理。公共接入API、Kratos内嵌组件边界、协议、业务逻辑、依赖与生成文件保持。没有新增helper、manager、生产依赖或性能机制。
- **工具核验**：本会话没有gopls MCP，使用已安装gopls v0.21.1 CLI核对Queue调用、frameAppender唯一实现、Carrier引用、callback fixture、PreparedProto.Reset、静态Discovery及resolveOptions调用链，并检查修改文件诊断。ast-grep MCP本次实际可用，成功查询两种transport的Queue构造；CLI也可用。初次位置/模式查询失败或无匹配不作为删除证据，修正后由语义查询与源码复核。不安装、升级或改配置，也不扩展到全库审查。

| 批次 / issue | 实际改动与复审依据 | 生产净减行 | 测试净减行 |
| --- | --- | ---: | ---: |
| I56 | Queue直接构造，保留PanicHandler和非法容量panic；同步两种Client及全部测试调用，队列执行/终止逻辑未动 | 26 | 0 |
| I57 | 默认protoFrameCodec分支直接使用同一proto.Size/MarshalAppend；保留具体类型判定、custom fallback和校验→Flush→编码→Write顺序 | 13 | 0 |
| I58 | Carrier原样内收到network私有类型，原两项测试迁入transport_test.go，连接ID常量自检改为真实Transport接线断言；TCP/WS用conn_id字面量 | 8 | 1 |
| I59 | 两种transport各自基础fixture增加可选opened通道，替换六个构造；Handle/Close、通道容量、同步发送与cleanup顺序保持 | 0 | 29 |
| I60 | 三个NATS场景共用一次broker/Bus/订阅装配；独立publisher超限、Bus.Publish边界/空payload、两次panic后正常返回及全部关闭统计断言保留 | 0 | 26 |
| I61 | 保留*preparedState，把Once移到PreparedProto；先创建state再Marshal，Reset清引用并重置Once；新增零值比较和map key编译检查 | 7 | -6 |
| I62 | 删除重复resolveOptions缺依赖测试，保留NewServer全部三种缺失输入与精确错误断言；失败前不调用Locator | 0 | 31 |
| I63 | 静态Watch直接持有捕获的slice，首次Next仍append到nil slice；删除中间Discovery，保留first/context/Stop与nil/空集合语义 | 0 | 16 |
| I64 | 清空原Certificates的Option迁入Node TLS握手测试，在ServerTLS应用后、服务构造前执行；保留真实生命周期和health RPC/关闭等待 | 0 | 33 |
| **合计** | **物理行，包含空白、注释、迁移与新增编译检查** | **54** | **130** |

范围仍为gateway、node、network、internal、locate、registry、event、instance，排除api、examples、独立test module和生成文件。实际为**23包、74生产文件/10,611行、102测试文件/19,716行、466个顶层Test函数**；对比基线净减1包、1生产文件、184总行、2生产类型（含1私有interface）、3测试fixture类型、1套原子缓存发布机制。只合并I60的2个顶层测试及I62/I64各1个，header测试迁移不重复计删除。I61保留使用后不得复制的说明和结果对象，不强求原8–10行估算；不宣称内存、吞吐或延迟改善。

| 本次在根目录执行 | 结果 |
| --- | --- |
| `golangci-lint fmt --config .golangci.yml <全部修改/新增Go文件>` | 已完成，只格式化任务Go文件 |
| `make check` | 通过：buf lint、两个module的tidy diff/vet/staticcheck/测试及工作树/暂存区diff检查。受影响包测试实际运行；日志中无变化包的`(cached)`为Go有效缓存复用，未重跑同配置普通测试 |
| `make lint` | 通过：根/test module均0 issues，新增与存量告警均为0 |
| `go test -race ./internal/queue ./network ./network/tcp ./network/websocket ./event/nats ./gateway ./node -count=1 -timeout=180s` | 七包全部通过；覆盖callback关闭、Prepared并发共享/旧bytes、TCP/WS默认与custom codec、Gateway广播顺序/停止、发现和Node TLS生命周期 |
| 本地文档路径/锚点、完整diff、新增文件、`git status --short`、`git diff --check`、`git diff --cached --check` | 已核对；九项状态与实际patch一致，暂存区为空 |

环境为Go 1.26.6 windows/amd64、golangci-lint 2.13.2；race命令内前置已有`D:\soft\msys64\mingw64\bin`。日志位于`%TEMP%\yola-i56-i64-20260926-9ca9ad7`的`check.log`、`lint.log`、`race.log`；临时日志不保证长期保留。本次未设置YOLA外部集成变量、仓库无TestMain；真实Redis/etcd、Cluster管理与完整游戏按条件跳过，不计为通过。NATS使用本机临时内嵌broker，TCP/WS/gRPC使用本机临时连接，由既有cleanup回收；未访问VM或操作既有服务。

I61的panic后状态由代码顺序复核：state在Marshal前创建，Once即使因panic完成，后续仍返回既有零值body/err，不引入nil解引用；这不是新增panic注入测试的通过声明。没有协议、服务入口或构建链改动，不触发breaking/build；没有运行benchmark或容量验收。P2、Drop及I03/I46保持原状态，本次九项无未完成项。用户随后授权提交全部现有改动，随本次清理提交归档；没有git push或发布。

提交前再次核对完整diff、当前规则及验证日志。Go源码、依赖、配置、工具与环境未变，本次只更新提交状态，复用上述check/lint/race结果；文档引用、工作树和暂存区diff检查重新执行。

## 三轮审查记录（实施前）

- **基线与范围**：2026-09-26，`ab0479b`。初次审查起点干净，后续保留前次未提交docs及AGENTS.md已有改动继续核对，Go源码不变；只审查根框架和有效测试密度。
- **结论**：没有可信的P0大块删除项。前两轮九项P1维持47–68生产行、114–145测试行的静态净减估算。第三轮无新增P1，只补充约4生产行、10–12测试行的可选P2和别名清理，不加入排期或抬高累计收益；见 [第三轮](./architecture-review.md#round3) 和 [累计估算](./architecture-review.md#10-缩减边界与不重复计算的估算)。
- **方法**：第三轮重读当前AGENTS与实际skills，重新发现MCP能力。gopls/ast-grep虽有本机配置，当前会话查询返回unknown MCP server；使用已安装gopls v0.21.1与ast-grep 0.45.3 CLI核实引用、实现和结构，没有改MCP配置。原24包、75生产文件、102测试文件统计因源码未变而沿用。
- **文档处置**：执行清单仍只保留I56–I64；补全I60的QueueDroppedCurrent=false断言，细化Kratos过滤/空发现/错误类型不能直接替换的依据。I61保留结果指针和公开可比较性，I03/I46仍是独立未解决问题；第一轮清理不重复计收益。
- **状态**：审查和文档已完成，随本轮提交归档；九项候选均未实施，没有修改生产代码、测试、协议、依赖或外部环境。
- **验证**：第三轮完成静态源码/引用/AST、固定依赖及文档检查；没有新跑框架测试、lint、race、benchmark或外部服务。第二轮的临时类型编译探针仅作为相同源码的既有证据，没有重跑或当作候选运行通过；当时反射探针的链接器错误仍保留记录。
- **授权**：用户在审查完成后授权提交本任务的docs修改；不包含已有AGENTS.md改动、后续代码提交、push或发布。第一轮代码清理为`ab0479b`，规则提交为`35341a7`，新窗口以实际HEAD和工作树为准。
- **并行变动**：收尾发现AGENTS.md及skills目录被其他操作更新，已重读当前规则；本任务仅修改docs，保留该AGENTS.md改动，不纳入本次交付归属。

<a id="cleanup-handoff"></a>
## 新窗口实施提示词

以下提示词已由本次用户授权执行完毕，保留用于追溯范围与验收条件，不再作为待办；本文档本身不授权执行或提交历史计划。

```text
请在 D:\src\pitaya\yola 实施已经完成三轮审核的框架清理，重点是去复杂、减代码、降维护成本。本次授权实施 docs/issues.md 中 I56–I64 九项 P1，不要继续进行无目标的全库审查，也不要只输出方案。

1. 先检查 git status --short、暂存/未暂存 diff、新增文件和当前 HEAD，保留用户已有改动，尤其 AGENTS.md。读取当前适用 AGENTS.md/AGENTS.override.md、docs/README.md、docs/issues.md、docs/architecture-review.md、docs/refactor-progress.md；按任务补读 architecture.md、eventbus.md。第一轮代码清理为 ab0479b，三轮审核的候选尚未实施；以当前代码复核，不重做已完成项。
2. 重新发现当前安装且可用的 skills/MCP，按需使用 gopls、ast-grep、并发与设计评审能力。上轮 gopls/ast-grep MCP 有配置但未进入会话工具表，CLI 可用；新窗口重新判断，不沿用旧失败结论，不为工具安装、升级或配置阻塞可完成的工作。
3. 按职责分批完成 I56 Queue直接构造、I57删除单实现frameAppender、I58 header包内收、I59 callback fixture复用、I60 NATS测试合并、I61 Once统一缓存发布、I62删除重复依赖测试、I63静态Discovery fixture简化、I64 TLS测试合并。先核对引用、调用链和验收条件，再实施；遇到代码事实推翻方案，修正/降级该项并说明，继续其他独立项。静态减量估算不是必须达到的指标。
4. 保持 gate（gateway包）/node 作为原生 Kratos v3 内嵌组件；保持行为、公共接入接口、协议、标识符、求值/短路、锁范围、defer、错误身份、nil/空集合、I/O时机及资源关闭顺序。候选明确允许的内部签名/类型调整须同步全部调用方。不改业务逻辑、性能机制、存储布局、依赖版本或生成文件，不新增无收益的helper/manager/BaseServer。
5. 必须保留的细节：I57按具体类型识别，不能以codec.Name替代；I59保持Open通道时序；I60保留独立publisher、Bus.Publish、两次panic后正常返回及全部统计断言，包括QueueDroppedCurrent=false；I61保留结果指针和公开可比较性，Once内先创建state再Marshal，保持懒编码、Reset无重叠及旧bytes不可变；I63在Watch捕获slice、在首次Next复制；I64在ServerTLS Option应用后、服务构造前修改调用者Certificates。
6. P2和Drop不自动实施；I03业务保活、I46 Redis deadline及性能/容量验收不在本次范围。必要的就绪屏障、条件注册、listener owner、两阶段排空、epoch监视、两种连接池与TCP/WS独立I/O语义保持不变。
7. 按当前AGENTS完成格式化、lint、受影响包测试或make check，以及适用race等检查；不重复make check已覆盖的同配置普通测试，不把历史通过作为当前结果。运行外部测试前核对TestMain、环境变量和隔离要求。确需真实依赖时可免密SSH root@192.168.152.129，先核对资源，只用任务专用可丢弃容器/端口/DB或prefix，结束后清理，不操作既有服务和数据。
8. 每批完整复审diff，更新issues状态和refactor-progress中的实际改动、验证、净收益、未完成项；所有工作完成后核对git status、git diff --check、git diff --cached --check。用简洁中文交付结果，不宣称未运行检查通过。本提示词不授权git commit、git push或发布，提交须等我另行明确指示。
```

## 第一轮清理记录

- **日期与基线**：2026-09-26，`af33b00`；开始时工作树、暂存区均为空。
- **轮次**：第一轮清理。
- **目标**：清理 yola 框架冗余，保持 gate/node 为 Kratos v3 内嵌组件；不改变行为、接口、协议、业务逻辑或执行时序。
- **状态**：代码及文档清理、必需验证和完整 diff 复审已完成，随本轮提交归档。
- **授权**：用户已授权提交第一轮清理；不包含 push 或发布。
- **规则**：已读取根 AGENTS.md、docs 和已安装 skills 的适用范围；开发类 skill 正文已核对。仓库无更近目录覆盖规则。

## Skill 取舍

| Skill | 本轮用途或不采用的原因 |
| --- | --- |
| codebase-design | 核对职责和抽象收益，保留真实所有者及边界转换，优先局部删除与直接表达 |
| code-review | 复审完整 diff、相关调用链和任务覆盖，重点核对错误、短路、锁与 defer |
| golang-lint | 沿用 `.golangci.yml` 和 Makefile，只格式化修改文件，区分新旧告警 |
| golang-concurrency | 核对回程池和 Node 失权路径的并发不变量，不新增并发机制 |
| golang-gopls、ast-grep | 按需导航；本轮引用和模式可由 rg 与源码直接核实，不为工具使用新增改名或改写 |
| diagnosing-bugs、domain-modeling、grilling | 当前未授权业务/故障行为变更，也无待决定的领域语义，不扩展为故障修复或访谈 |
| 文档产物、图像、浏览器及插件管理类 | 已核对安装目录与适用范围；本轮为仓库 Markdown/Go 清理，无对应产物需求 |

## 实施与验证状态

| 批次 | 实际改动 | 状态 |
| --- | --- | --- |
| 文档筛选 | 删除 I36/I08/I29/I04/I34 的候选方案与排期，保留行为边界；I40/I41/I45 收敛为部署/验收限制；I03/I46 未关闭 | 已实施 |
| Node | 注册声明集中到 register.go；删除已被前置条件排除的 NodeID 空值判断；失权分支线性化，局部名称区分请求和回复 | 已实施 |
| Gateway 与回程池 | 展平解绑错误日志及连接池命中分支；删除 resolver 的单次纯构造 helper | 已实施 |
| 格式、check、lint、race | 按仓库验证矩阵执行；make check 覆盖受影响包普通测试，不重复运行相同配置 | 通过 |
| 真实依赖 | 专用 Redis/etcd 验证 Gateway/Node 接入与注册交错，不访问既有服务数据 | 两个定向用例 race 通过 |
| 完整 diff | 核对移动声明、短路、错误、资源时序及文档链接；检查工作树和暂存区 | 已复审 |

资源和工具：Go 1.26.6 windows/amd64、golangci-lint 2.13.2；race 在当前命令内前置 MSYS2 GCC 路径。远端 `192.168.152.129` 使用 root 免密 SSH，Docker 29.8.0；专用 Redis 8.6.1、etcd 3.5.21 分别限制 0.5 CPU/128MiB、0.5 CPU/256MiB，使用 tmpfs，仅开放 VM 回环 16379/12379，经本任务隧道访问。

## 第一轮验证记录

以下均在根目录执行，针对 `af33b00` 加本轮工作树，不复用历史文档的通过结论。

| 命令或核对 | 结果 |
| --- | --- |
| `golangci-lint fmt --config .golangci.yml gateway/auth.go gateway/resolver.go internal/gateclient/client.go node/dispatch.go node/register.go node/session.go` | 通过，只格式化六个修改的 Go 文件 |
| `make check` | 通过：两个 module 的 tidy diff、vet、staticcheck、测试及根 buf lint；包含受影响 package 的普通测试 |
| `make lint` | 通过：根 module 与 test module 均 0 issues，无新增或存量告警 |
| `go test -race ./gateway ./node ./internal/gateclient -count=1 -timeout=180s` | 三个受影响包通过 |
| `go test -race -p=1 ./gateway ./node -run '^(TestGatewayNodeIntegration\|TestAppPreparedNodeCannotOverwriteReplacement)$' -count=1 -timeout=90s -v` | 设置专用 Redis/etcd 地址后，两个真实依赖用例通过 |
| docs 相对路径、锚点与问题状态核对 | 七份文档的本地引用无断链，保留十个已关闭问题与 I03/I46 两个未解决问题 |
| `git diff --check`、`git diff --cached --check` 与完整 diff 复审 | 通过；无暂存或新增文件，改动限定六个框架 Go 文件和六份 docs |

普通 check/race 未设置外部环境变量，受条件控制的集成测试会跳过；这些跳过不算通过，真实依赖证据来自上述单独命令。未运行 Cluster 管理回归、完整游戏/容量压测或性能 benchmark；未修改入口、构建链、依赖或根协议，不触发 build/breaking。

复审确认注册声明只在包内迁移，泛型约束、公开标识符、panic 和 middleware 顺序不变；early return 保持错误短路与日志/取消顺序，回程池保持锁范围、引用计数与 timer 操作。NodeID 冗余判断的删除依赖紧邻的非空 claim 前置分支，不移除 epoch 或存储 fencing。

收尾核对容器完整 ID、名称、任务 label 与 tmpfs 后仅删除本任务的两容器，按 PID 和完整转发参数核实并停止隧道。Redis DB 0/9 和 etcd 测试 prefix 收尾为空，任务 label 下无残留容器；既有 Redis、etcd、NATS、MySQL、Consul 未修改。

<a id="next-actions"></a>
## 后续边界

本轮完成验证与复审后交付，不自动继续 I03/I46、容量优化、协议增强或业务重构。没有新的、收益明确的等价清理证据时停止扩展范围。后续任务须以届时用户指令、Git 和当前契约重新定界。

## 历史记录（不作为当前计划）

以下保留原修复、失败与验证证据；其中“本轮”“下一步”“已授权”均指各自历史任务，不能据此提交、启动新修复或宣称当前代码已通过。历史日期、临时产物、工具和配置不保证仍可直接复用。

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

<a id="redis-deadline"></a>
### Redis deadline 历史诊断（未修复）

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
