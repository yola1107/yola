# 08：收紧路由职责与测试依赖

Status: resolved
Blocked by: 07

用户要求继续深度审核高内聚、低耦合与整洁架构，收拢大量测试文件。沿用 ADR 0001：官方 Kratos 不动，保留多 Gateway/Node、TCP/WS、普通 Kratos 服务及独立 Sender。

本轮起点是任务 07 完成后的未提交工作区，不是单独的 HEAD `30ba7aa`。开始前已保存源码与文档快照，路径记录于 `/tmp/yola-cohesion-baseline-path`；保留全部既有改动，本轮不提交。

## 审核结论与范围

### Gateway：路由决定与执行归同一 owner

Server 当前直接读取 backend 快照、解释 sticky、查 NodeBinding/epoch，并把 NodeID 分别放入 RPC 字段、context 与 WaitForReady。同一不变量跨越协议适配和路由实现。

将两个解析步骤移入现有 backends，返回私有路由值；该值保存已解析的 GateBinding/NodeID/epoch 并执行内部 RPC。Server 继续管理客户端协议、请求预算及原有两阶段错误和日志。复用现有 NodeLocator，不增加公开 interface 或新的路由层。

比较后采用保留“解析→RPC”两阶段的设计。直接合为一个 forward 调用需要额外暴露错误阶段；只移动解析 helper 又会留下精确路由规则散落在 Server。私有路由值能同时保留现有行为并集中该规则。

Gateway 测试按认证、租约、Node 路由、投递、客户端流水线、启动/停止组织。搬空并删除 `server_test.go`、`session_test.go`、`timeout_test.go`、`balancer_test.go`；`heartbeat_benchmark_test.go` 归为 `lease_test.go`。其余按行为接收用例，生命周期启动与停止用例分开放置，预期 19→15 个测试文件。

### Node：测试只依赖所需能力

内存夹具当前实现完整 Locator，包括永远 panic 的 Gate 写方法。绑定、epoch 测试和故障 adapter 改为依赖 Pinger＋NodeLocator；Push 所需 Gate 查询在本地夹具显式提供。删除无用的 Gate 写能力和共享 Gate map。

保留 Node 的 11 个测试文件。它们对应注册编解码、请求路由、epoch、绑定、副作用、生命周期及不同真实依赖。真实 Redis 迟到写不能由内存夹具替代。

### 网络与 NATS：共用行为测试，缩小重复准备

- TCP/WS 的回调 FIFO、队列溢出关闭、push→kick→disconnect 三组契约集中到 `network_test`，各执行两个真实 adapter。替换原来的六段重复场景；adapter 特有的错误和 I/O 交错留在各包。
- 两个 adapter 的 `stats_test.go` 归入各自 `channel_test.go`；WebSocket codec 识别和大小校验归到 codec 测试。
- NATS 满队列重复场景的慢消费者错误、丢弃后继续订阅断言迁入现有 backlog 测试，再删除重复场景。subscription 的容量观测测试与队列/排空测试放在一起，删除搬空的 `stats_test.go`。

保留 TCP/WS 的监听与连接 adapter、共享协议模块、epochLease、gateclient，以及 NATS 协议屏障、真实权限错误、独立发布进程 benchmark。没有证据支持新增通用 runtime、testkit 或抽象层。

## 验收

- [x] Server 不再读取 backend 快照、拼装精确 Node 路由；解析与 RPC 的错误映射、日志阶段和调用顺序保持不变。
- [x] 粘性目标失效不改投；只有 Forward 查询 epoch，Disconnect 保持原路由语义。
- [x] Node 测试不再依赖无关 Gate 写方法；原有绑定、推送、失权与 Drain 场景保留。
- [x] 纯移动用例核对包路径、名称和主体；被共享契约或 NATS 场景替换的断言逐项对应，保留两种真实 adapter。
- [x] 文件分组按稳定职责，未把不同资源 owner 合并进巨型测试文件。
- [x] 必要包测试、race、lint、两个 module 检查及真实分布式验收通过；完成 code-simplifier、独立双轴复审与 retro。

## Comments

调查、设计和实施使用 `gpt-6.1-sol/xhigh` subagent，按 Gateway、Node、网络/NATS 分工；主 agent 负责方案选择、跨目录集成、最终深度审核与文档。上一轮验证只复用于输入未变的部分。

### 完成结果

2026-10-18 完成。相对任务 07 工作区，本轮只修改 `gateway/backend.go`、`gateway/forward.go`、`gateway/server.go` 三个生产文件；其余为测试和文档。

- `gateway/backend.go:106`、`:152`：backends 解析归属，nodeRoute 统一携带本次绑定和 Node 凭据并执行 RPC。`forward.go:25` 保留原解析错误与 RPC 错误的两阶段处理。没有新增网络查询或公开 interface。
- `node/test_helpers_test.go:90`：内存夹具仅持 Node 状态；删掉 Gate map、三个假写方法、Gate 查询与 setGate，改由 `node/push_test.go:447` 的局部目录提供查询。七个故障 adapter 收紧依赖，真实 epoch/Drain 场景保留。
- `network/client_test.go:20`：三个公共回调契约各运行 TCP、WebSocket。关闭通过公开 ConnectionHandler.Close 观测，替换旧的六段实现耦合测试。
- `event/nats/subscription_test.go:258`：队列、排空与统计就近维护；合法 Bus.Publish、慢消费者错误、丢弃后继续订阅的独有断言已移交。

| 范围 | 测试文件 | Test/Benchmark 声明 | 处理结果 |
| --- | --- | --- | --- |
| Gateway | 19→15 | 122→122 | 认证、租约、路由、投递、流水线与生命周期分别归组 |
| Node | 11→11 | 89→89 | 收窄夹具依赖，共用声明归 helper，局部故障夹具留原文件末尾 |
| network（含子包） | 28→27 | 162→159 | 六段回调主体改为三个双 adapter 契约；发送统计归 channel |
| event/nats | 11→10 | 49→48 | 满队列重复场景合入 backlog；统计归 subscription |

四个范围共 69→63 个测试文件，测试源码净少 171 行。减少的是重复描述和碎片；六个真实 TCP/WS 回调场景仍逐项执行。Gateway 普通测试文件为 267–506 行，共用资源夹具另放 helper；没有把生命周期全部堆入一个文件。

### 等价证据

- Gateway 271 个测试函数及 helper 完整保留；统一 import alias 与构造注入后，266 个函数 AST 相等，五个预期差异为依赖迁移或行为观测增强。新增错误阶段断言在生产变更前通过：Locator 返回 ResourceExhausted 仍映射 Unavailable，RPC 返回同码则原样保留。
- Node 89 个 Test、503 条断言、19 处 t.Run 表达式完整保留。规范化重命名后，79 个 Test 主体相同，另外十个仅调整依赖准备；177 个声明的纯移动前后格式化源码相同。
- 网络/NATS 十个纯移动 Test 的源码及 AST 相同。七个旧函数由三个公共契约和已有 backlog 场景承接，逐项核对触发路径、屏障、断言与失败清理。
- NATS backlog 先确认队列深度为 2，再执行合法 Bus.Publish，并精确观察丢弃 3 条；保留 ErrSlowConsumer、继续 Subscribe、并发统计及关闭后累计值。容量 1 的场景仍由 NativeClosed 覆盖。

临时证据：`/tmp/yola-cohesion-gateway-equivalence.md`、`/tmp/yola-cohesion-node-evidence.log`、`/tmp/yola-cohesion-network-assertions.md`、`/tmp/yola-cohesion-network-equivalence.log`。主 agent 另核对全范围声明清单与 import 路径；新增依赖为零。

### 验证

全部通过：

- `make check`、`make race`：覆盖根目录和 test 两个 module。日志 `/tmp/yola-cohesion-check.log`、`/tmp/yola-cohesion-race.log`。
- Gateway、Node、network 和 NATS 的包测试、race、golangci-lint；lint 均为 0 issues。gopls 无错误，保留未改 broadcast benchmark 的 b.Loop 建议。
- 公共回调契约与 NATS backlog/native-drop 定向 `-race -count=20`。日志 `/tmp/yola-cohesion-network-contract-race20.log`。
- 设置 `YOLA_REDIS_INTEGRATION=127.0.0.1:16379`、`YOLA_ETCD_INTEGRATION=127.0.0.1:12389`，使用专用实例验收：多 Gateway/Node TCP/WS、跨 Gate 重连、粘性目标退出不改投、独立 Sender、目录隔离及旧 epoch 替换，`-race -count=10` 通过。日志 `/tmp/yola-cohesion-distributed.log`。
- `go test -race ./node -run '^TestLateBindingWritePreservesReplacement$/standalone' -count=10 -timeout=180s`：真实 Redis 迟到绑定写入通过，覆盖三种写入和两种替换目标。日志 `/tmp/yola-cohesion-binding-write.log`。
- diff check、改动文档的相对链接检查通过。Kratos、kratos-layout 工作区干净，go.mod/go.sum 未改。

本轮没有重跑真实游戏机器人、Redis Cluster 拓扑/故障注入和大规模容量测试；真实游戏的上一轮结果保留在任务 07，不计入本轮验证。当前改动以包行为、真实路由集成及存储迟到写验证；后续结果见 [Cluster 任务](../../redis-cluster-topology/spec.md)，容量要求见[容量验收](../../../docs/project/performance.md#容量验收)。

### Standards

独立 `gpt-6.1-sol/xhigh` reviewer 审读快照至工作区的全部 42 个变化文件，含新增、删除测试与文档。依据 AGENTS、README 测试约定、领域文档、ADR 与 code-simplifier：0 个硬规范问题，0 个需修改的 smell。确认 route 所有权、夹具能力、helper 布局及资源清理符合现有约定。

### Spec

另一位独立 `gpt-6.1-sol/xhigh` reviewer 审读同一完整差异：0 个遗漏、越界或错误实现。独立核对 Node 被删除的 Stateful 设置原先没有有效 epoch lease；公共回调的六个真实场景、NATS 原调用路径及失败清理均保留。

主 agent 复审生产分支、所有行为变化及机械迁移证据，未发现待修问题。code-simplifier 在各目录实施后与汇总时完成。本轮未提交；专用 `yola-cohesion-redis`、`yola-cohesion-etcd` 容器已删除。

### Retro

按优先级记录本轮环境与流程结论：

1. `make check`、`make race` 已覆盖两个 module，仍缺 CI/hook 自动执行；真实依赖仍需显式启用。继续采用[原复盘](../retro.md)的环境改进建议，不复制检查入口或向 AGENTS 添加规则。
2. 替换测试需核对“触发路径＋独有断言”。本轮发现 raw Publish 与框架 Bus.Publish 虽可能得到同一队列结果，却验证不同调用路径，已保留后者并加满队列屏障。此类判断交给 Spec 复审和迁移对照表。
3. 批量移动先保留原 import 身份，再用现有编译、lint 和依赖差异检查验证。一次 goimports 自动选择了同名旧版本包，已在局部编译阶段修正；主 agent 又比对 import 路径集合，确认未新增依赖。
4. 存在未提交成果时先保存本轮快照。把纯移动判等与行为差异分开审读，能够保留前一轮改动并聚焦实际风险；临时迁移工具留在系统临时目录。

本轮的结构收益是路由规则集中、测试依赖收窄，以及同一客户端契约只描述一次。epoch、连接代次、双阶段排空和不同 adapter 的 I/O owner 继续承担现有分布式约束。
