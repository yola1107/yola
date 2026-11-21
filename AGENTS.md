# 开发约定

yola 是基于 Kratos 的长连接框架。根模块维护框架和最小示例，`test/` 是完整游戏和集成测试的独立 Go 模块。

## 范围

- 只修改任务涉及的内容，清理本次改动留下的无用实现。
- 文档和说明性注释使用中文短句，标识符和工具标记保留原文。
- 接口契约以 `.proto` 源文件为准。API、配置或行为变化时，同步更新相关文档和示例。

## 按需阅读

- 编写或审查 Go 代码前，读 [Go 编码规范](docs/agents/go-styles.md) 和 [.golangci.yml](.golangci.yml)。该配置是 lint 与格式规则的唯一来源。手写 Go 代码禁止任何形式的 `//nolint`。
- 探索代码、讨论领域概念或设计功能前，按[文档导航](docs/README.md#文档导航)和[领域文档约定](docs/agents/domain.md)查阅相关资料。
- 读写本地任务或规格时，遵循[任务与规格约定](docs/agents/issue-tracker.md)。
- 提交前，读[提交约定](docs/agents/commit.md)。

## 完成条件

- 修改 proto、Ent schema 或 Wire provider 后，运行对应生成命令。
- 修改手写代码后，先执行[完成前清理](docs/agents/code-cleanup.md)，再做最终验证。
- 修改 Go 后，在根目录依次运行 `make fmt`、`make lint`、`make check`。并发改动另在所属模块对受影响包运行 `go test -race`。
- 全仓以 lint 零告警为目标。修复本次引入的告警，报告范围外的历史告警，集中治理须单独授权。
- 修改 lint 规则须先确认误报并说明项目级理由。禁止为消除告警而关闭检查或扩大排除范围。修改 lint 配置后运行 `make lint-config`。
