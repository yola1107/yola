# 13：收拢 Gateway 连接与认证准入

Status: resolved
Blocked by: 12

基线 `e26ce50`。用户确认本轮架构方案：实施 Gateway 本地状态收拢和 Whot 装配减法；Node/Sender 推送共用暂缓。沿用 `refactor/root-test-suite`；后续获授权提交并合并到本地 main。

## 范围

- `sessionRegistry` 统一拥有连接表、准入状态和已接纳认证计数，删除独立 admission、Open 双检回滚及跨锁 current 核验。
- registry 只处理本地资格；Server 保留认证、BindGate、补偿与接管，lifecycle 保留进程资源管理。I/O 和等待在 registry 锁外执行。
- 关闭准入后不新增认证；正常排空期间已接纳认证仍可提交和接管，预算耗尽后保留原有拒绝与补偿。保留连接代次、租约、条件解绑及超时后的同一清理所有者。
- Whot 示例删除未使用的 GateDirectory，更新现有示例与架构说明。
- 优先复用 Open/Handle/Stop 行为测试。直接操作 admission 计数的用例改用实际阻塞认证，或在已有场景接替断言后删除。
- 不改 Kratos、Node/Sender 推送契约或其他架构。`test/` 不参与搜索、修改和验证；不新增通用抽象或逐方法测试文件。

## 验收

- [x] 连接登记与认证准入在同一所有者内决定，独立 admission 和双检补偿消失。
- [x] 认证、Close/Stop、接管与迟到回收契约保留；缺失的 Open/Stop 交错覆盖使用可控同步。
- [x] Whot 装配与文档一致，既有无 GateDirectory 的 Session.Push 用例通过。
- [x] 根 module 测试、受影响包 race/lint、示例编译通过；隔离 Redis/etcd 的分布式回归实际执行。
- [x] code-simplifier、Standards/Spec 独立复审、最终修复和 retro 完成。

## Comments

实施与独立复审沿用 `gpt-6.1-sol/xhigh` 子代理。主 agent 负责文档、依赖环境、集成验证和最终审核。重构先复用已有行为基线；不为未改变的契约制造失败测试。

### 结果与覆盖

- registry 共用短锁决定登记、认证计数增加和关闭准入。删除独立 admission 类型、原子标志及跨锁 current 协议；认证与停机继续在锁外执行远端 I/O 和等待。
- 保留 `quiesce → takeAll`。正常排空可完成接管；集合移交后的迟到认证仍拒绝并条件解绑，最终由同一停机过程汇合。
- Whot 删除 GateDirectory；现有 `Session.Push` 行为不变。更新 README 和架构说明，未增加公开接口、生产文件或通用模块。

| 用例调整 | 接替的行为 |
| --- | --- |
| `TestQuiesceHonorsAdmissionDeadline` 替换为 `TestShutdownTimeoutCleansLateAuthentication` | 用真实阻塞 BindGate 替代手动增加 WaitGroup；观察内部预算、连接先关闭、迟到 Aborted、绑定补偿与最终 Stop 返回。 |
| 删除 `TestStopWaitsWhenCallerHasNoDeadline` | 上述迟到认证场景以 Background 调用 Stop；既有 `TestStopTimeoutKeepsOneCleanupForLaterCallers` 保留重复 Stop 和唯一清理断言。 |
| 认证拒绝、已接纳接管两个既有用例 | 改走 Stop，保留不产生新绑定、认证成功和旧连接关闭，补充明确的 SessionReplaced Kick。 |
| 新增 `TestOpenConcurrentWithStopRetainsAcceptedConnectionForDrain` | 在 Connection 的 ConnID 调用处建立屏障，与 Stop 交错，检查连接关闭、Shutdown Kick、登记清除及停机后拒绝。 |

### 验证

基线及实施后的定向普通测试通过。定向 race、七个 Gateway 文件的 gopls 诊断通过。最终执行：

```sh
go test -json -count=1 -timeout=240s \
  ./api/... ./event/... ./examples/... ./gateway/... ./instance/... \
  ./internal/... ./locate/... ./network/... ./node/... ./push/... ./registry/...
go test -race -json -count=1 -timeout=180s ./gateway
golangci-lint run --allow-parallel-runners ./gateway ./examples/whot
```

- 根 module 26 个含测试包全部通过，其余六个包完成编译。Gateway 完整 race 通过；普通与 race 均实际执行双 Gateway、双 Stateful Node、TCP/WS 重连归属和独立 Sender 场景。lint 为 0 issues。
- 单独复用 `TestStatefulSessionPushDoesNotRequireGateDirectory`，并完成 Whot 编译。最后只调整新增夹具换行和文档措辞；gofmt 与 diff 检查通过，复用此前功能验证。
- Redis 7.4.9、etcd 3.5.30 使用临时容器、动态本地端口及命令内 Colima context。清空测试子进程的外部 NATS、Redis 密码和 Cluster 配置；NATS 使用包内 broker。结束后仅删除本任务两个容器。
- 未执行 Redis Cluster 迁槽/切主、容量 benchmark 或 `test/` module；未改对应实现。Makefile 检查会进入排除目录，因此使用显式根包范围。

完整普通与 race 日志位于 `/var/folders/dz/h_3sbvgs2yd5bt20y9dxvg8m0000gn/T/yola-admission-hqlxwz2_/`，lint 日志为 `/tmp/yola-admission-lint.log`。

### 复审与 retro

- code-simplifier：复审全部触点，展开多行结构体与新增同步流程，保留必要并发屏障，未增加转发层或重复测试文件。
- Standards：0 项遗留。Spec：缺失、越界及实现错误均为 0。主 agent 核验锁顺序、Add/Wait、超时补偿和删测映射；不采用改变迟到认证行为的延后 takeAll 方案。
- 环境建议沿用已有 [retro](../retro.md)：为现有检查配置 CI，并单独运行真实依赖验证。本轮集成测试明确记录执行而非跳过，未新增平行检查脚本或 AGENTS 规则。
- 本轮经验：先区分正常排空和预算耗尽后的收尾，再迁移状态所有权，避免结构重构隐式改变成功/失败语义。
