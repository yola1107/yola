# 开发约定

本项目是基于 Kratos 的长连接框架。根目录与 `test/` 是独立 Go module，分别维护框架及最小示例、完整游戏及集成测试。

## 范围

- 只做当前任务所需的改动，优先复用现有代码和配置；新增抽象、配置项、脚本或依赖须有实际需求。
- 不顺手重构、清理或格式化无关代码；清理本次改动产生的无用代码。
- 业务语义或接口契约有歧义时，先说明并确认；普通实现细节沿用现有约定。

## 文档

- 文档和说明性注释使用中文短句，标识符和工具标记保留原文。
- API、配置或行为变化时，同步更新相关文档和示例；接口契约以 proto 源文件为准。

## 代码质量

有手写代码改动时，在最终验证前执行一轮 [完成前清理](docs/agents/code-cleanup.md)，保持行为不变。

## 完成条件

- Go 改动后，在根目录运行 `make fmt`、`make lint` 和 `make check`，覆盖两个 module；并发改动加跑受影响 module 内相关包的 `go test -race`。
  修改 lint 配置时另运行 `make lint-config`。自动修复可用 `make lint-fix`，修复后审阅差异并重新验证。
- 全仓保持 lint 零告警，未通过时继续修复。
- [.golangci.yml](.golangci.yml) 是 lint 和格式规则的唯一来源，按规则修复代码。
  调整规则须有项目层面的理由，确认误报后再改，不靠关闭检查或扩大排除范围。
- 手写 Go 代码一律不写 `//nolint`，指定 linter、整文件抑制或附带理由的写法也不例外。
- 修改 proto、Ent schema 或 Wire provider 后，运行对应生成命令。
- 先明确可验证的完成条件再执行检查；报告实际执行的检查，失败或未验证的部分如实说明。

## 按需阅读

- 编写或审查 Go 代码前，读取 [Go 编码规范](docs/agents/go-styles.md) 和 [.golangci.yml](.golangci.yml)。
- 探索代码、讨论领域概念或设计功能前，按 [文档导航](docs/README.md#文档导航) 和 [领域文档约定](docs/agents/domain.md) 读取相关资料。
- 读取、创建或更新任务及规格时，遵循 [任务与规格约定](docs/agents/issue-tracker.md)。
- 提交前，读取 [提交约定](docs/agents/commit.md)。
