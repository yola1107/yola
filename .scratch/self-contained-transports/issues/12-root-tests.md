# 12：全面精简根 module 测试

Status: resolved
Blocked by: 11

用户要求使用 Matt 工作流深度精简全部 `*_test.go`，删除冗余和非必要代码，提高清晰度。基线为 `22a134b`，在 `refactor/root-test-suite` 实施。`test/` 目录排除于搜索、分析、修改和验证；收尾按用户授权提交，未推送。

## 范围与判断标准

覆盖根 module 的 80 个测试文件：Gateway / Node 23 个，network 26 个，event / Redis / registry / push 20 个，其余 internal / locate / instance / examples 11 个。按文件完整检查，生产代码仅为判断契约而读取。

- 保留公开行为、错误边界、并发交错、失败清理、真实存储契约及有独立用途的 benchmark。
- 删除或合并用例须记录原触发路径、独有断言及接替覆盖；仅覆盖相同代码行或名称相近不足以证明重复。
- 优先删除无用 fake 能力、重复准备、纯转发层和重复场景。按同一职责收拢测试，保持用例在前、helper 在后。
- 测试必要性以当前契约和回归风险判断，不以文件数、行数或覆盖率设削减目标。无充分证据的场景保留并记录原因。
- 不修改生产行为、依赖、接口或官方 Kratos，不新增通用 testkit。

## 验收

- [x] 全部 80 个文件完成检查，记录各范围的改动与保留依据。
- [x] 每项用例删除、合并都有行为与断言对照；夹具简化保留同步和回收语义。
- [x] 受影响包检查及根 module 全部测试通过，按风险运行 race、lint 和必要真实依赖验证。
- [x] code-simplifier、Standards / Spec 独立复审、主 agent 最终审核及 retro 完成。
- [x] 改动仅限范围内测试和本任务记录，明确未执行的检查。

## Comments

流程：完整盘点 → 依据契约选择删改 → 分目录实施 → 验证 → code-simplifier → 双轴复审 → retro。沿用用户指定的 `gpt-6.1-sol/xhigh` subagent；主 agent 负责候选选择、跨目录核对和最终审核。

### 结果

完整检查原有 80 个文件。修改 39 个测试文件，新增一个包内共享夹具文件；41 个文件保持原样。测试代码由 21053 行变为 20586 行，Test 入口由 497 个变为 483 个，16 个 benchmark 全部保留。这些数量只记录结果，删改依据见下表。

| 检查范围 | 实施与保留依据 |
| --- | --- |
| Gateway / Node：23 文件 | 修改 14 文件。收拢重复推送、注册冻结、绑定归属及租约预算场景；删除构造失败前不会使用的资源与重复 fake。保留 epoch、连接代次、迟到写入、失权、排空和请求顺序等独立交错。 |
| network：26 文件 | 修改 15 文件。真实 TCP/WS 矩阵接替重复 callback fake；普通启动和 socket 回收归入既有 helper。TLS、帧编码、队列、超时、迟到 Accept/Upgrade/Open、Serve 失败及重复 Stop 的差异均保留。 |
| event / Redis / etcd / push：20 文件 | 修改 7 文件，新增 push 共享夹具。删重复 Bus 生命周期入口，合并 etcd worker 关闭断言，简化幂等放行。Redis 五文件及独立容量测试保留；存储原子性不能由上层替身测试接替。 |
| internal / locate / instance / examples：11 文件 | 修改 3 文件。queue 复用现有提交与等待 helper；gateclient 将辅助类型移到用例之后；修正示例的陈旧 etcd key 期望。其余验证取消优先级、监听所有权、TLS 克隆、路由转换、输入边界、认证和元数据，均有独立用途。 |

`push/test_helpers_test.go` 集中两个测试文件实际共用的构造、RPC 夹具和资源回收。`newTestSender` 接替 16 处成功构造及 Close 注册；错误构造测试仍直接调用公开 API。未增加通用 testkit，也未为减少文件数混合不同职责。

### 用例删除与接替

下列名称均可在所属包定位。合并前对照触发路径、断言、同步和失败清理；生产代码只用于判断等价性。

| 删除或合并项 | 保留的行为与接替位置 |
| --- | --- |
| node：`TestSessionPushAndClose`、`TestPushToUIDLocatesGateAndPushes` | `TestClientMiddlewareObservesPushWithoutChangingResult` 的 session/uid 两行走相同真实 RPC，检查 body、command、BindingToken。重复 Stop 和停止后 Forward 拒绝归入 `checkAcceptedPushDuringStop`，由两个既有推送排空用例执行。原 handler 自身返回字面值的断言由 `TestClusterForward` 的真实返回值透传覆盖。 |
| node：`TestPushToUIDRejectsAfterStop` | `TestBusinessDrainCanPushToUID` 在 Stop 后检查同一 Unavailable 及精确错误消息。 |
| node：`TestStartingFreezesHandlersAndRejectsReentry` | `TestStopDuringInitialRenewalReclaimsEpoch` 在同一初次续租屏障后检查两种 handler 注册拒绝与重复 Start 错误，再验证 Stop 回收。新增断言前已注册有界清理。 |
| node：`TestHandlerRegistrationRejectsAfterStart` | `TestRegistrationDerivesOnlyInternalNodeRecord` 在准入开放后验证冻结；stateless/stateful 两行均覆盖。 |
| node：`TestForwardValidationAndErrors` | `TestClusterForward` 从公开 RPC 入口检查缺 BindingToken、未知 command、handler 错误身份；错误码及原返回值断言保留。 |
| node：`TestSessionUnbindDoesNotDeleteNewNode` | 原续租替换用例改名为 `TestSessionBindingPreservesReplacement`。先验证续租不能覆盖 node-b，再独立验证旧 Session 解绑不能删除 node-b，最后保留主动接管。 |
| node：`TestMapGatePushErrorPreservesContextAndStatus` | `TestClientMiddlewarePreservesErrorIdentity` 经 Session.Push 检查 canceled、deadline、Aborted 和原 ResourceExhausted 的错误对象身份。 |
| gateway：`TestRPCTimeoutDoesNotCancelLeaseRenewal` | `TestLeaseTimeoutPreservesShorterParent` 保留 1ns RPC 预算、实际续租预算与更短 parent 两行，并接替 leaseDeadline 向后推进断言。默认值仍由 options 测试验证。 |
| gateway：`TestSlowForwardDoesNotBlockHeartbeat` | `TestDefaultHeartbeatSurvivesExtendedForward` 使用同一个 helper、相同断言与释放流程，保留 TCP/WS 默认周期。原快项仅改变正时长，没有独有分支。普通与 race 均实跑默认慢场景。 |
| tcp/ws：两个 `TestClientDisconnectCallbackCanCloseClient` | 收拢到 `network/client_test.go` 的真实 TCP/WS 矩阵；分别有界等待外层 Close 与 disconnect 回调内重入 Close 完成，失败清理也有界。 |
| tcp：`TestPushReportsQueueAndClosedState` | `TestSendStatsQueueAndClose` 检查精确满容量、原消息指针、done=nil、重复 close 与 closed 错误，同时保留统计断言。 |
| nats：`TestBusLifecycle` | `TestBusPublishSubscribe` 验证真实交付；`TestBusContextCancellationClosesBusAndSubscriptions` 验证显式 Close、重复 Close 及关闭后 Pub/Sub 错误；默认 Background 的显式关闭仍有独立用例。 |
| etcd：`TestRegistryClientCloseStopsLeaseWorker` | `TestRegistryCloseStopsKeepAlive` 接替“注册 context 取消不停止续租”和“client 关闭后 worker 退出”，并保留 key 按 TTL 消失。真实 etcd 验证迁入断言后删除旧入口。 |
| nats：六个 setter 子用例 | `TestOptionSetters` 顺序应用相同 setter，对完整 options 逐字段检查原值。TLS 所有权和配置错误测试保留。 |
| tcp/ws：reader 的 rejected 表行 | 原 NewClient 认证拒绝用例经过相同 adapter，检查同一 sentinel 和 nil client。错误操作码、push limit、损坏帧和 WS 非二进制输入仍分别保留。 |
| ws：oversized response 表行 | 与 push 行调用同一 SendProto、使用不读取 Op 的固定长度 codec。保留 push 行及走不同 CloseWithProto 路径的 final 行；TCP 的独立 reply 路径保留。 |

### 夹具与断言收紧

- 普通 server 启停复用既有 helper，保留 Start 结果检查及 worker 汇合。socket dial 成功后立即登记回收；用于触发断连或释放名额的正文 Close 保留。所有 12 个 TCP、14 个 WS dial 调用点均核对清理顺序。
- 两个阻塞 lookup 用例在 `newTestSender` 返回后登记 unblock，保证失败时先放行再 Close。Redis 并发绑定的 WaitGroup 保留，避免 Fatal 后遗漏 worker 汇合。
- 删除 `tlsBackendNode`、`queueFullConnection`、`invalidNodeAppInfo`、`invokeConnection`、两个 TCP Open fake、一个 WS Open fake，复用已有能力；删除未被调用的 App 工厂可选 server 参数。
- queue 删除纯转发 `waitSignal` 和两处没有后续断言的末尾 Stop；既有 cleanup 仍停止并汇合 worker。gateclient 仅调整声明顺序，函数体不变。
- Node advertise-host 用例补合法 Registration 并精确断言目标错误，避免因缺少 Registrar 而误绿。
- 真实 etcd 检查发现 `examples/env/env_test.go` 仍期望旧目录。现按既有 `NewNodeRegistry` 契约检查 `/microservices/yola/nodes/<service>/<id>`，保留 JSON 和 GetService 一致性断言；未改生产实现。
- 未合并独立 cleanup 预算测试：Kick 最终得到 DeadlineExceeded 不能证明清理开始时拥有足够预算。WS full/closed 的短用例也保留，避免引入条件准备表。

### 验证

按以下 26 个显式包执行，未调用会进入排除目录的 Makefile 目标：

```sh
packages=(
  ./event ./event/nats ./examples/env ./examples/gateway ./gateway ./instance
  ./internal/clusterroute ./internal/contextwait ./internal/gateclient
  ./internal/grpcendpoint ./internal/listener ./internal/queue ./internal/tlsconfig
  ./locate ./locate/redis ./network ./network/internal/auth
  ./network/internal/heartbeat ./network/internal/host ./network/internal/inbound
  ./network/internal/request ./network/tcp ./network/websocket ./node ./push ./registry/etcd
)
go test -count=1 -timeout=240s "${packages[@]}"
go test -race -count=1 -timeout=300s "${packages[@]}"
golangci-lint run --allow-parallel-runners "${packages[@]}"
```

- 普通检查首轮 25 包通过，仅 examples/env 因旧 key 期望失败；修正后该包及 middleware 用例复验通过。最终全 26 包 race 通过，含真实 Redis、etcd、默认心跳、分布式 Gateway/Node 和 Sender 场景。
- lint 全 26 包为 0 issues；复审修复后对 examples/env、node 补跑 lint。全部修改测试通过 gofmt 与 diff 检查。生产与 module 文件的基线 SHA 校验无变化。
- 复用各组具名普通检查；网络关闭/清理相关用例 race 重复 20 轮，Gateway/Node 相关用例 3 轮，均通过。
- `go test -run '^$' -bench '^BenchmarkWebSocketServer$' -benchtime=1x -count=1 -timeout=60s ./network/websocket` 通过，验证迁移后的 benchmark 回收；单次执行不用于性能结论。
- 真实依赖为本任务临时容器：etcd 3.5.30、Redis 7.4.9，均使用动态本地端口，无持久卷。子进程清空 Redis 密码、外部 NATS 与 Redis Cluster 变量，NATS 使用测试内 broker。检查结束后已删除这两个临时容器；未改全局 Docker context 或原有容器。
- 明确跳过：Redis Cluster 迁槽/故障切换及两处 cluster 行；NATS 容量子进程入口在普通测试中按设计跳过。未执行 Linux RSS 容量测量、大规模 benchmark 或排除的 `test/` module。

普通、race、lint 原始日志分别保存在 `/tmp/yola-alltests-test.jsonl`、`/tmp/yola-alltests-race.jsonl`、`/tmp/yola-alltests-lint.log`；修复复验见 `/tmp/yola-alltests-review-fixes.log` 与 `/tmp/yola-alltests-review-fixes-lint.log`。

### 独立复审与 retro

- Standards：用户指出 NATS options 多字段同行降低可读性，主 agent 展开该结构及共享 GateBinding 结构。独立 reviewer 复核后无遗留问题。
- Spec：首轮发现 Stop 契约混入 middleware 用例；移回既有推送排空流程后复审关闭。其余删测映射、错误身份、同步和清理未发现覆盖缺口。两路均以 `22a134b` 对工作区 diff 和新增文件复审。
- code-simplifier 与主 agent 最终审核完成。保留必要换行、可读的短表及专用并发屏障；精简依据是重复职责与无用准备。

复盘按优先级记录：

1. 真实 etcd 暴露了普通环境会跳过的陈旧示例期望。沿用已有 [retro](../retro.md) 的专用真实依赖检查建议；本轮不新增 CI 或改变检查范围。
2. 现有 AGENTS 已明确清晰度高于简短。本轮未充分落实，合并审查应同时核对断言归属和排版可读性；不再向 AGENTS 重复追加同一规则。
3. 分组先做完整盘点、再审批删改候选，成功保留独立 cleanup 预算与 Redis worker 汇合。证据应随删测记录触发、断言和资源回收，避免以覆盖率或行数替代判断。
