# 08：按职责组织代码与阅读流程

Status: resolved
Blocked by: None

## 验收

- [x] 覆盖两个 module 的代码及测试，保留开始时全部未提交改动。
- [x] 结合 golang-code-style 与 code-simplifier，改善相关类型、构造函数、方法及辅助函数的阅读顺序。
- [x] 保留当前 AGENTS.md 的明确约定与测试关键前提，不为形式统一增加抽象、拆文件或改变行为。
- [x] 完成必要检查及全部任务增量复审，不提交或推送。

## Comments

使用上游 `golang-code-style` 原文，分文件组织、控制流和测试表达并行只读审查，由主 agent 统一修改及复审。
继承任务 07 的明确配置和隐私测试准备；本轮不修改 AGENTS.md、协议、依赖或官方 Kratos。
保留 nil/empty、错误身份、求值及随机调用顺序、锁/defer 范围和外部 I/O 顺序。

上游依据：[文件组织](https://github.com/samber/cc-skills-golang/blob/main/skills/golang-code-style/SKILL.md#code-organization-within-files)。仅实施有阅读收益的等价变换；不为参数数量新增 options 类型，也不将 nil 改为空集合。

三路审查覆盖 311 个手写 Go 文件；控制流复核 2,836 个函数中的 160 个候选，测试复核覆盖 133 个测试文件。未变逻辑复用任务 07 已审内容，并核对全仓源码哈希。12 个生成文件及协议、构建、依赖输入保持不变。

执行期间用户删除 AGENTS.md 的统一前置规则。已重读并按最新规则复核；本轮未修改 AGENTS.md。

本轮调整 24 个 Go 文件：

- 将 11 处远离实现的接口检查移至对应类型旁；主类型同一声明区内的检查保持原位。
- 四个状态文件恢复 Status、StageID、Stage 和结算对象各自的类型与方法段。
- GameCards 的类型、构造和方法连续排列；GameProbe 方法与 codec 实现分组；mailbox 队列方法与 call helper 分组。
- 展开复杂租约表项和长调用、签名，参数及表项顺序不变。
- 显式呈现 Whot 并发加载屏障，删除只返回 repo 字面量的构造 helper；复用 Ludo 已有的准备状态常量。

主 agent 已复审全部增量；文件组织与测试两路另行对照开工快照复审通过。AST/token 对照确认包级声明内容、变量初始化顺序、保留的函数签名及生产函数体不变，698 个测试和基准等入口及顺序保留。两个测试体的差异分别为构造 helper 等价内联、值为 2 的既有常量替换。

`make check`、`make lint`、`make race` 全部通过。首次 lint 报多行签名后的空行，修正后两个 module 均为 0 issues；该纯空行修改复用此前 check，最终 race 使用修正后的源码。真实 Redis/etcd 和完整分布式对局本轮未重跑，生产执行逻辑未改。

证据目录：`/var/folders/dz/h_3sbvgs2yd5bt20y9dxvg8m0000gn/T/yola-style-cohesion-st4enyvi/`。保存开工快照、上游原文、移动计划、等价对照及检查日志。未暂存、提交或推送。

用户指出 WebSocket 客户端选项布局后，对照本地 Kratos 的 gRPC、HTTP 客户端及应用选项。三者没有统一的声明顺序或闭包排版。
`network/websocket/client_options.go` 采用 gRPC 客户端的布局：ClientOption、With 函数、私有配置结构、配置解析；撤回任务 07 对私有结构的前移，并展开 With 闭包。
复审确认相对 HEAD 仅有闭包换行，选项顺序、赋值、默认值及校验逻辑不变。`go test ./network/websocket` 和该文件的 `git diff --check` 通过；本次未重跑全仓检查。
