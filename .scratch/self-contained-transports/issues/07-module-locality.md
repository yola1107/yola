# 07：收拢内部流程与文件职责

Status: resolved
Blocked by: 06

基线：`30ba7aa`。用户要求继续审视架构与流程，实施有依据的文件合并和删除。遵守 AGENTS 与 ADR 0001，保持公开接入、分布式路由、错误优先级和外部行为。

## 实施范围

- Node：每个 admission 自管未开放、开放和终态，移除 Server 上重复的准入开关；请求与副作用继续分别排空。三种绑定写入共用私有保护流程。
- Gateway：删除测试专用排空入口和同义测试 interface；迟到认证的连接池回收归唯一 shutdown owner。
- 合并 `node/session_test.go` 到 `binding_test.go`、`node/server_test.go` 到 `options_test.go`、`gateway/distributed_test.go` 到 `integration_test.go`。保留用例与真实依赖行为。
- Redis 脚本与各自 Gate/Node 操作相邻，公共脚本执行放回 locator；合并发送观测声明到 Connection 文件。删除原 `script.go`、`network/stats.go`。
- 保留 resolver/balancer、epochLease、gateclient、TCP/WS adapter、真实 Redis 延迟写测试及历史验收文档。仅减少文件跳转的变化不宣称架构或性能收益。

## 验收

- [x] 启动前拒绝请求；终态不可重新开放；Drain 期间绑定和推送仍可继续。
- [x] 三种绑定写入保留配置检查、准入、身份/epoch、取消、存储动作和错误转换的原顺序。
- [x] Gateway 超时后的迟到认证、排空、网络关闭与重复 Stop 仍有唯一回收 owner。
- [x] 合并前后测试名称与场景完整保留；脚本文本及初始化表达式不变。
- [x] 受影响包测试/race/lint 与两个 module 的集成检查通过；code-simplifier、双轴复审和 retro 完成。

## Comments

调查由三个 `gpt-6.1-sol/xhigh` subagent 分别覆盖 Gateway、Node/Sender 和外围文件结构。主 agent 选择可证明收益的改动并负责最终复审。工程分工按目录隔离，共用当前工作区；本轮提交未获新的明确授权。

### 实施结果

2026-10-18 完成。五个文件按上述范围归并后删除，公开 API 与接入步骤不变。

- `node/admission.go:26` 将阶段与计数归到同一对象；删除 Server 的两个重复开关。Start 只开放一次，Stop 或失权后无法重开。请求与副作用各自排空。
- `node/session.go:79` 将三种绑定写入的共同保护步骤收拢到 `writeBinding`。方法表达式保留 nil Locator 检查的优先级。
- `gateway/lifecycle.go:101` 删除 quiesce 超时后的旁路回收 goroutine；`shutdown:284` 在关闭网络后等待迟到认证、关闭连接池，再汇合迟到 Stop。
- 删除生产代码中仅供测试调用的 `drainSessions`，测试直接使用实际排空路径；删除同义 `applicationServer` interface 和重复 `waitNodeSignal` helper。
- Lua 与各自 Gate/Node 操作相邻；发送观测声明与 Connection 相邻。修正架构文档和历史报告的对应链接。

Node 测试文件 13→11，Gateway 测试文件 20→19。文件减少的收益是同一职责就近维护；运行时简化来自准入状态、绑定写入和回收责任的集中。

### 等价与验证

按包路径与函数名核对基线和当前 Test/Benchmark 声明：Node 89→89，Gateway 121→122，network（含子包）162→162，Redis 27→27；没有删除旧声明。Gateway 新增的远端接管与迟到 Stop 顺序回归，在基线 overlay 和当前实现均通过。迁入 Node 的五个测试函数源码逐字相同；Gateway 两个集成场景只调整依赖准备与首个 err 声明。

Redis 45 个声明、network 8 个声明的规范化 AST 前后相等；九个 Lua 脚本原文、SHA-256 与完整初始化表达式相等。`redis.NewScript` 初始化只做本地哈希和对象分配，移动声明不改变外部初始化行为。

验证全部通过：

- `make check`：两个 module 的依赖差异、vet、staticcheck、测试以及 Buf lint、diff check。日志 `/tmp/yola-locality-check.log`。
- `make race`：两个 module 全包 race。日志 `/tmp/yola-locality-race.log`。
- Node、Gateway、Redis 和 network 各受影响包的独立测试、race、golangci-lint；lint 均为 0 issues。Node/Gateway gopls 无诊断。
- 设置 `YOLA_REDIS_INTEGRATION=127.0.0.1:16379`、`YOLA_ETCD_INTEGRATION=127.0.0.1:12389`，指向本轮专用实例；下列验收命令均通过：

```sh
# 根 module；两个环境变量分别指向上面的专用实例。
go test -race ./node ./gateway ./registry/etcd \
  -run 'TestDistributedGatewaysKeepNodeOwnershipAndIndependentSender|TestGatewayNodeIntegration|TestAppPreparedNodeCannotOverwriteReplacement|TestNodeDirectorySeparatesApplicationAndEnvironment' \
  -count=10 -timeout=180s

# test module；沿用同一环境变量，使用默认 30 秒场景。
go test -race -p=1 ./ludo/tools/press ./whot/tools/press \
  -run '^TestConfiguredGameDelivery$/(smoke|robots)$' -count=1 -timeout=180s
```

分布式验收覆盖 TCP/WS、多 Gateway/Node、跨 Gateway 重连、原 Session 失效、固定 Node 退出后的拒绝改投，以及普通 Kratos 服务使用独立 Sender。日志 `/tmp/yola-locality-distributed.log`。Ludo 与 Whot 的 smoke/robots 分别用时 68.262s、84.840s，日志 `/tmp/yola-locality-games.log`。

本轮没有重跑 Redis Cluster 拓扑、故障注入或大规模容量验收；存储协议只做已证明等价的移动。后续结果见 [Cluster 任务](../../redis-cluster-topology/spec.md)，容量要求见[容量验收](../../../docs/project/performance.md#容量验收)。

### Standards

独立 `gpt-6.1-sol/xhigh` reviewer 检查全部已跟踪差异和本任务票。依据 AGENTS、领域文档、ADR 与 code-simplifier：0 个硬规范违例，0 个需要处理的 smell。准入状态和绑定 helper 收拢了现有职责，未增加推测性抽象；合并夹具保留依赖隔离和正确清理顺序。

### Spec

另一位独立 `gpt-6.1-sol/xhigh` reviewer 检查同一基线：0 个需求或正确性问题。核对了三态终止、Drain 期间副作用、绑定写入顺序、迟到认证回收，并独立比对声明与 Lua 等价证据。主 agent 复审全部差异、未跟踪文档与动态结果，确认本票验收完成。

code-simplifier 已在各目录实施后及汇总复审时完成。Kratos 与 kratos-layout 工作区均保持干净；本轮未提交。专用 `yola-locality-redis`、`yola-locality-etcd` 容器已删除。

### Retro

按优先级记录环境改进，复用已有入口和文档：

1. 已有 `make check`、`make race`，仍未找到 CI 或有效 Git hook；真实依赖验收仍靠手工设置变量。这两个高优先级缺口与 [上一轮复盘](../retro.md) 相同，建议接入已有检查和专用集成任务，本轮不重复建立规则或脚本。
2. 文件迁移的临时验证应按“包路径＋函数名”统计。TCP/WS 存在同名测试，单用函数名会合并计数；本轮已修正统计并确认全部原声明保留。纯移动继续用 AST/源码判等，再复用已有行为测试。
3. 本轮目录分工清晰，适合在同一工作区并行；交叉文件由主 agent 独占。验证日志独立落盘、只回报结果和失败上下文，便于汇总且减少输出噪声。

无需增加 AGENTS 规则或新的文档层级。实现证据、复审和复盘留在本票；长期契约留在架构文档与 ADR。
