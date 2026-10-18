# 02：继续全仓行为等价清理

Status: resolved
Blocked by: 01

## 验收

- [x] 检查两个 module 的全部手写代码与测试，记录有收益的简化及保留项。
- [x] 遵循当前 AGENTS 对多行结构体定义、字面量逐字段换行的要求；保留所有字段的顺序、类型和标签。
- [x] 保留上一轮全部未提交内容及用户对 AGENTS 的已有修改，不改公开契约、业务和并发时序。
- [x] 按第二轮实际增量验证、复审并记录结果，说明复用的检查。

## Comments

起点为任务 01 的工作区结果。第二轮开始前另存 Go 文件与任务记录快照，用于区分本轮增量；用户新增的 AGENTS 规则保持原样。范围继续包含 test/、全部 *_test.go；生成代码与归档报告不手工改写。不提交或推送。

第二轮完整复核 311 个手写 Go 文件；12 个生成文件保持原样。根 adapter/transport 的 84 个文件没有收益明确的新候选，保留协议、取消、写完成和所有权屏障。

本轮仅修改 10 个已有 Go 文件，没有新增代码文件或测试：

- 展开三组结构体定义字段；顺序、类型、标签和布局保持一致。AST 扫描未再发现手写多行结构体内的合并字段或同行字段。
- Ludo 将 moveOne 原实现收回公开 Move，搜索直接调用 Move。私有函数仅两个调用；原函数体在参数名映射后相同。
- Ludo reconnect 删除仅用于重复空 token 校验的私有参数。唯一调用方 Login 在调用前保留拒绝分支，其余重连流程原样。
- 压测金额复用 RandFloat 的退化区间处理。ParseToken 已拒绝非有限数并保证 Max>=Min；等值仍零次采样，正常范围仍一次。保留局部变量及其后的独立赋值。
- Whot 手牌整数排序使用 slices.Sort；DRAW/SKIP 合并返回，并缓存一次 Action，保留求值及动作选择顺序。
- 三个测试关闭屏障使用 OnceFunc，保留调用、defer、cleanup 和断言。

未继续合并：配置解析的阶段 helper、service adapter、mailbox 的不同等待/入队契约，以及含独立同步窗口的 transport 分支。函数短或外形相似不足以证明它们冗余。

本轮快照与日志位于 `/var/folders/dz/h_3sbvgs2yd5bt20y9dxvg8m0000gn/T/yola-simplify-pass2-k21kkub6/`。第一轮变更完整保留。本轮未修改 AGENTS.md，保留用户执行期间补充的规则和措辞。

定向 Ludo、Whot 测试及两个 module 的 `make check`、`make lint`、`make race` 全部通过。日志为快照目录内的 `check.log`、`lint.log`、`race.log`；退出码记录在 `checks.json`。

独立复审对照本轮快照检查全部 10 个文件，零发现。主 agent 复核完整增量，确认 698 个测试、基准等入口的包与名称集合不变；没有增删 Go 文件，gofmt 与 diff 检查通过。协议、生成文件及依赖文件均无改动。证据见 `final-audit.json`。

隔离 Redis/etcd 下重新执行受影响的真实对局，默认 30s 观察窗口、race 下 Ludo/Whot 各自的 smoke、robots 共 4 个子场景全部通过，无跳过。在 test module 执行：

```sh
go test -race -json -p=1 -count=1 -timeout=300s ./ludo/tools/press ./whot/tools/press -run '^TestGameDelivery$/(smoke|robots)$'
```

命令、退出码与子场景记录在 `integration.json`，完整输出为 `game-integration.jsonl`。复用任务 01 的分布式 Gateway/Node、TCP/WS、Sender、Socket 顺序/重连和请求超时验证：本轮这些生产路径未变。当前预算配置保持原样，其覆盖也复用任务 01；本轮游戏增量另由上述真实对局验证。

未运行 Redis Cluster 管理用例和容量基准，不提出性能或容量结论。两个自建容器及临时 AST 工具已回收，快照与日志保留；未提交或推送。

复盘：优先消除已证明冗余的流程，保留有独立契约的短函数。随机边界和并发屏障先确认求值、所有权及调用顺序，再简化。

后续用户授权提交全部改动。AGENTS 规则调整与全仓清理分别提交，不推送。
