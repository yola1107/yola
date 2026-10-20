# 09：按主流程收紧职责与阅读层次

Status: resolved
Blocked by: 08

用户要求继续简化架构与测试文件，保持高内聚、低耦合，并把必要主流程放在辅助流程之前。沿用 [ADR 0001](../../../docs/adr/0001-self-contained-transports.md) 和 [规格](../spec.md)，公开行为与分布式能力保持不变。

本轮基线是任务 08 完成后的未提交工作区，快照路径记录于 `/tmp/yola-flow-baseline-path`。保留任务 07、08 的全部成果；本轮不提交。

## 审核与实施范围

### 主流程与职责

- Gateway：生命周期先展示 Start/Stop；认证、投递、路由和广播各自按入口、流程、底层支撑组织。生命周期类型归 lifecycle，认证准入归 inbound。backends 的快照 helper 下移，不挡住路由主线。
- Node：生命周期和 epoch 先展示所有者的主要操作，随后展示 I/O、续租和校验。仅由 Forward 使用的 command context 元数据归 dispatch；Session context 留在 session。
- WebSocket：`readBoundedFrame` 与 `errNilPayload` 归 codec，Channel 保持 socket、deadline、buffer pool、发送及关闭的所有权。
- NATS：Bus.Close 就地汇总自己持有的订阅退出结果，删除只在此处调用的跨文件 `waitSubscriptions`。
- Node 的 `options.resolveEndpoint`、`epochLease.releaseLocked` 合入各自唯一调用者。保留选项校验顺序、nil endpoint 分支及 epoch 原锁域。
- TCP/WS、NATS、独立 Sender 与 gateclient 的入口先于支撑细节；同级操作和同一类型的方法仍保持分组。

不引入新 runtime、interface 或兼容层。Node epoch、双阶段排空、连接代次和 adapter 的不同 I/O 语义仍有独立职责；保留其实现与测试。

### 测试组织

| 范围 | 本轮计划 | 原因 |
| --- | --- | --- |
| Gateway | broadcast benchmark 并入 broadcast_test，15→14 个文件 | 同一广播契约与夹具，无构建或资源隔离要求 |
| Node | register_test 并入 dispatch_test；admission_test 并入 lifecycle_test，11→9 | command 注册到分发、准入到排空分别构成完整流程 |
| network | WebSocket config_test 并入 channel_test，27→26 | Channel 配置校验与同一 owner 的测试就近维护 |
| NATS | 保留 10 个文件 | payload、activation、async error 和容量用例各有原始协议或进程隔离需求 |

WebSocket 有界读帧用例随实现归 codec_test。测试与 benchmark 入口在前，局部夹具在后；Gateway 单文件使用的 blockingBackendDiscovery 移回 backend_test。

本轮保留全部 Test/Benchmark 与独有断言，不按行数或文件数量强制合并。纯声明移动逐项判等；三处内联单独核对调用顺序、返回值、锁和等待。

## 验收

- [x] 入口、主流程、支撑流程层次清楚，同一 owner 的数据与规则就近维护。
- [x] 三处单调用 helper 删除，原错误身份、校验顺序、锁域和关闭顺序不变。
- [x] command 元数据、WebSocket framing 与认证准入归属清楚，无公开接口或依赖变化。
- [x] 测试文件按计划合并，全部 Test/Benchmark 和纯移动声明等价；失败清理保持有效。
- [x] 受影响包、两个 module 必要检查通过，纯移动与行为验证证据分开记录。
- [x] 完成 code-simplifier、Standards/Spec 独立复审、主 agent 最终深审和 retro。

## Comments

使用 Matt 的 improve-codebase-architecture / codebase-design 调查和选择方案，golang-refactoring 实施等价整理，最后执行 code-simplifier、code-review 和 retro。三个 `gpt-6.1-sol/xhigh` agent 分别负责 Gateway、Node、network/NATS，主 agent 负责 Sender/gateclient、文档、集成与最终审核。

相比把全部公开函数机械置前，本轮按实际调用层次组织：配置解析仍是配置文件的主流程；类型声明可先于入口，同级方法连续。相比进一步抽象，三个单调用 helper 直接合入 owner；相比大文件拼接，仅合并同一契约的测试。

### 完成结果

2026-10-18 完成。相对本轮快照共 51 个变化文件，包含删除、未跟踪文件与文档。

- `gateway/lifecycle.go:55`、`:143`：Start/Stop 位于准备与回收细节之前。认证准入操作归 `inbound.go:231`；接受开关和排空状态仍归 lifecycle，锁和等待语义不变。
- `node/lifecycle.go:23`、`:72`：启动与停止入口相邻；`node/dispatch.go:81` 收纳 Forward 的 command 元数据。`node/options.go:49`、`node/epoch.go:95` 完成两处内联。
- `network/websocket/codec.go:55`：有界读帧及其错误归 codec；Channel 继续持有连接、buffer 和关闭状态。`event/nats/bus.go:188` 就地汇总本 Bus 的订阅退出结果。
- `push/sender.go:44`、`internal/gateclient/client.go:57`：构造、推送与关闭入口连续，准入、连接缓存和错误辅助随后展开。
- 当前阅读顺序见[声明与函数布局](../../../docs/harness/rules/uber-go/style.md#function-grouping-and-ordering)，测试 helper 规则见[设计与测试约定](../../../AGENTS.md#设计与测试约定)。

| 范围 | 测试文件 | Test/Benchmark |
| --- | --- | --- |
| Gateway | 15→14 | 122→122 |
| Node | 11→9 | 89→89 |
| network（含子包） | 27→26 | 159→159 |
| event/nats | 10→10 | 48→48 |

合并范围共 63→59 个测试文件，418 个 Test/Benchmark 完整保留。真实 Redis/etcd、NATS 原始协议与容量进程测试继续独立维护。没有新增生产层次或公开接口。

### 等价与验证

主 agent 独立提取 Gateway、Node、network、NATS、Sender 和 gateclient 的声明：1743→1740 个。1737 个保留声明的格式化源码相同；只删除三个既定 helper，并将原逻辑合入三个调用者。上述范围全部 445 个 Test/Benchmark 及测试辅助声明保持原文。子 agent 另核对 AST、import 身份和注释；移动未改变常量或初始化依赖。

三个内联分别核对：

1. endpoint：选项应用、解析与错误包装、非 nil 时追加选项、Registrar 必填与默认地址的顺序不变。
2. epoch release：先停止续租，再进入同一 mutex/defer 锁域；保留停止错误、重复释放和存储失权分类。
3. Bus.Close：全部 beginStop → 全部 deactivated → 关闭连接 → publishDone → 按原顺序等待 stopDone 并汇总错误。

全部通过：

- `make check`、`make race`：根目录和 test 两个 module；日志 `/tmp/yola-flow-check.log`、`/tmp/yola-flow-race.log`。
- 受影响包普通测试、race、golangci-lint 与 gopls；lint 为 0 issues。保留广播 benchmark 原有的 b.Loop 建议。
- 公共 TCP/WS 回调契约和 NATS native/activation/ACL/统计/broker 上限场景分别 `-race -count=20`；日志 `/tmp/yola-flow-network-callback-race20.log`、`/tmp/yola-flow-network-nats-race20.log`。
- 最后 codec 声明顺序调整后，TCP/WS 包测试、lint 与 gopls 再次通过；其余检查通过声明等价复用。
- 文档相对链接、diff check 通过。go.mod/go.sum 未改；官方 Kratos、kratos-layout 工作区干净。

临时证据保存在 `/tmp/yola-flow-{gateway,node,network}-equivalence.log`、`/tmp/yola-flow-all-{before,after}.json` 与 `/tmp/yola-flow.diff`。本轮未运行真实 Redis/etcd 分布式验收、游戏机器人、容量 benchmark 和故障注入；任务 08 的分布式与迟到写结果作为既有证据保留，不计入本轮执行。当前验证覆盖等价移动、原位内联与既有包契约；部署要求见 [NATS 验收](../../../docs/project/eventbus.md#部署验收)和[容量验收](../../../docs/project/performance.md#容量验收)。

### Standards

独立 `gpt-6.1-sol/xhigh` reviewer 审读最终 51 个变化文件。规范违规 0、需处理的代码异味 0；主流程层次、测试归并、局部夹具归属和文档一致。code-simplifier 在实施结束和独立复审时完成。

### Spec

另一位独立 `gpt-6.1-sol/xhigh` reviewer 审读同一完整差异。需求遗漏、越界与错误实现均为 0；独立重扫确认声明与全部测试保留，三处内联等价。主 agent 最终审核无待修问题；本轮未提交。

### Retro

1. 现有 `make check`、`make race` 有效，但仓库仍没有 CI 或启用的 hook 自动执行。继续采用[原复盘](../retro.md)的自动检查建议，不再复制规则或检查入口。
2. 一次临时搬移工具误把 Go 字节偏移当作 Python 字符偏移，gofmt 当场拒绝。仅恢复该工具刚改动的 options.go，改用 UTF-8 字节截取；最终完整声明判等确认既有成果保留。后续临时搬移先在临时输出中 parse/gofmt，再写回源码。
3. 阅读层次交给独立 reviewer 按实际调用关系判断；编译、格式化和声明判等负责验证机械移动。现有文档只新增一条源码导航约定，不加入一刀切的公开函数排序规则。
4. 最后 code-simplifier 的纯排序修改使评审清单多出一个文件。已刷新完整差异并通知两位 reviewer；后续在源码冻结后生成评审清单，收尾文档由主 agent 复核。
