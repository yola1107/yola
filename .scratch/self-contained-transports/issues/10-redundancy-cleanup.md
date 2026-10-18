# 10：清理重复准备与多余分支

Status: resolved
Blocked by: 09

用户调用 code-simplifier，要求清理冗余、提高清晰度并减少不必要的复杂性和嵌套。检查范围为当前未提交改动；以任务 09 完成后的工作区为基线，快照路径记录于 `/tmp/yola-cleanup-baseline-path`。保留全部既有成果，本轮不提交。

## 实施范围

- TCP `CloseWithProto` 保留原关闭条件与错误，合并重复返回。
- NATS `deactivate` 对空 native 提前返回，减少嵌套和中间变量；`consume` 合并相邻退出条件，保持短路顺序。
- Node `Stop` 在入口归一化 context，启动清理和等待复用同一值。
- Gateway 测试复用既有 GateBinding 转换；Node 测试复用 testNodeContext；TCP benchmark 复用既有帧构造 helper，共用 helper 归现有 test_helpers_test.go。

保留实际并发屏障、幂等关闭、错误优先级和有明确职责的辅助函数。不删测试或公开接口，不增加抽象，不继续调整文件阅读顺序。

## 验收

- [x] 分支简化保持调用次数、短路、返回错误和资源回收顺序；重复 context 归一化例外已由纯函数语义确认。
- [x] 测试准备的数据和隔离语义相同，所有 Test/Benchmark 与断言保留。
- [x] 受影响包测试、race、lint 和最小 benchmark 验证通过，完整本轮差异复审通过。

## Comments

Gateway/Node、network/NATS 分别由 `gpt-6.1-sol/xhigh` agent 按 code-simplifier 审查和实施。主 agent 检查 Locator、Sender、gateclient 及跨目录一致性；这些范围未发现值得修改的冗余。

2026-10-18 完成，共修改 10 个 Go 文件及两份任务文档。四个生产函数完成局部清理：`event/nats/subscription.go:73`、`:154`，`network/tcp/server_connection.go:248`，`node/lifecycle.go:72`。其余为既有测试准备的复用和 helper 归位。

主 agent 对基线核对 1871 个声明，无新增或删除，仅十个预期函数主体变化。全部测试名称和断言保留；六字段路由、context 构造和帧编码逐项相同。独立 `gpt-6.1-sol/xhigh` reviewer 审读全部 12 个变化文件，未发现行为、职责或规范问题，主 agent 最终复审通过。

验证通过：

- `go test ./gateway ./node`、对应 `-race`，以及局部 golangci-lint。
- `go test ./network/tcp ./event/nats`、对应 `-race`，以及局部 golangci-lint。
- TCP Codec 与 1000 连接、32 字节 payload 的 request/push benchmark 各执行 `-benchtime=1x`；仅验证夹具可运行，不用于性能结论。
- 在 `test/` 执行 `go test ./...`，验证应用 module。
- gofmt、diff check、文档相对链接通过；依赖未改，官方 Kratos 和 kratos-layout 工作区干净。

证据位于 `/tmp/yola-cleanup-gateway-node-{equivalence,test,race,lint}.log`、`/tmp/yola-cleanup-network-{review,test,race,lint,benchmark-codec,benchmark-server}.log`、`/tmp/yola-cleanup-test-module.log` 与 `/tmp/yola-cleanup.diff`。

本轮以局部等价证明、包测试和应用测试验收，未重跑真实 Redis/etcd 分布式场景、游戏压测或容量测试；原验证记录仍保留在前序任务。本轮未提交。

复盘：现有 helper 已足够承接重复准备，先核对实际数据和调用路径再复用。没有新增环境改进项，继续采用[原复盘](../retro.md)的自动检查建议。
