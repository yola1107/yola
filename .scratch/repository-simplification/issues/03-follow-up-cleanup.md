# 03：提交后继续全仓等价清理

Status: resolved
Blocked by: 02

## 验收

- [x] 复查两个 module 的手写代码与测试。
- [x] 仅实施有明确清晰度收益且能确认等价的简化。
- [x] 保留 AGENTS.md、docs/README.md 的用户已有修改。
- [x] 完成必要检查及全部任务差异复审。

## Comments

基线为 `8711c10`，分支为 `refactor/root-test-suite`。
工作区已有用户对 AGENTS.md 和 docs/README.md 的修改。
本轮不改写这两个文件。

范围包含全部 `*_test.go` 和 `test/`。
按当前 AGENTS 检查入口、主流程、同级操作和辅助流程的顺序。
不为制造差异重排职责已经清楚的代码。
保留行为、测试覆盖和生成文件。
不提交或推送。

本轮复查 311 个手写 Go 文件。
12 个生成文件保持原样。
非 Go 配置、协议输入和归档报告未变，复用前两轮审查结果。

| 范围 | 手写 Go 文件 | 调整文件 |
| --- | ---: | ---: |
| 根 adapter、transport | 84 | 3 |
| 根核心、内部组件、示例与 API | 73 | 8 |
| Ludo、test/internal | 98 | 8 |
| Whot、test/gateway | 56 | 9 |

28 个文件仅调整函数声明顺序。
没有新增或删除源码文件。

- Gateway/Node/Sender 的公开选项先于解析、校验辅助。
- Gateway 停止流程与 backend 操作保持连续。
- Registry 构造、关闭和配置选项分别连续。
- Queue、Mailbox、日志及 Redis 组件的入口先于辅助。
- 游戏业务和压测入口集中，装配、等待和状态辅助后移。
- 两个 Whot 测试文件的 helper 放到用例之后。

保留路由、租约、排空、协议适配及游戏算法。
未发现值得继续改写的语义冗余。
不按公开/私有机械排序，也不合并有独立契约的短函数。

AST 及原文对照覆盖全仓 3,937 个函数，其中手写函数 2,835 个。
每个函数的签名、函数体及关联注释逐字节一致。
非函数声明的内容及顺序不变。
全部注释文本不变。
初始化顺序和编译指令未变。
698 个测试、基准等入口的包、名称及文件内顺序不变。
AGENTS.md 与 docs/README.md 和本轮起点快照一致。

证据目录：`/var/folders/dz/h_3sbvgs2yd5bt20y9dxvg8m0000gn/T/yola-simplify-pass3-y9hlwnai/`。
`ast-audit.json` 记录等价性核对。
`function-order.json` 记录调整前后的函数顺序。
各分区定向普通测试、gofmt 和 diff 检查通过。
两个 module 的 `make check`、`make lint`、`make race` 全部通过。
`checks.json` 记录命令退出码和日志路径。
独立复审全部 28 个 Go 增量，零发现。
主 agent 已逐份复核全部差异。
最终源码哈希与复审版本一致。
临时 AST 工具已归档到证据目录。
未暂存、提交或推送。

复用任务 01、02 的真实依赖验证结果。
本轮仅移动完整函数声明，函数内容与初始化顺序未变。
因此未重启 Docker 或重复运行分布式连接和游戏对局场景。
Redis Cluster 管理用例和容量基准未运行。
本轮没有性能或容量结论。

复盘：阅读顺序按职责分组，而非机械地按导出状态排序。
完整函数移动优先使用原文对照验证。
