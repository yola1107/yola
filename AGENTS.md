# AI 开发指南

Yola 是基于 Kratos 的长连接框架；根目录与 `test/` 是独立 Go module。

按任务需要查阅[项目文档](docs/README.md)。

维护本文件和 docs 文档时，使用简短、准确、易读的句子。同一规则只保留一处。

## 范围与授权

- 搜索、分析、修改和验证均遵守任务范围。涉及排除目录时限定文件或包，无法覆盖的检查须说明缺口。
- 评审、诊断和规划默认只读。实施任务在授权范围内自主完成修改、验证和复审，范围外问题只报告。
- `git commit`、`git push` 和发布须明确授权。
- 用户指令优先于本文件；本文件优先于外部指南和 Skill 通用约定。Skill 不扩大授权，个人工具偏好不作为执行前提。
- **凭据**：发现明文密钥、Token 或密码时报告位置与风险，不复制、扩散或擅自修改相关逻辑。

## 代码规范

Go 代码遵循 [Uber Go](https://github.com/uber-go/guide/blob/master/style.md)、[Effective Go](https://go.dev/doc/effective_go) 和 [Go Code Review Comments](https://go.dev/wiki/CodeReviewComments)。

- 优先级：安全 > 清晰度 > 简单 > 短。
- 保持职责集中、主流程平直，减少重复逻辑和嵌套。
- import 分组以 [.golangci.yml](.golangci.yml) 为准。别名仅用于消除冲突或明确区分领域、版本。
- 按业务用途命名，使用 MixedCaps 和一致的缩写。
- 注释用简洁中文解释意图、约束或所有权，省略对代码的复述。

## 工作流程

参考 [Karpathy Guidelines](https://github.com/forrestchang/andrej-karpathy-skills/blob/main/skills/karpathy-guidelines/SKILL.md)：

1. 开始前执行 `git status --short`，区分并保留已有改动。
2. 明确需求、关键假设和验收标准，澄清影响实现的歧义。
3. 优先复用现有实现，只做满足当前需求的必要改动。
4. 按改动影响选择必要检查。复审全部任务改动（含未跟踪文件），确认满足验收标准。
5. 用简洁中文报告结果、验证情况和剩余风险，说明复用或未完成的检查及原因；代码结论附 `path:line`。

### 提交约定

- 仅暂存已复审的任务改动。每次提交一个原子改动，源文件与生成文件一起提交。
- 提交格式为 `<type>(<scope>): <中文摘要>`，type 使用 `feat|fix|refactor|docs|test|chore`。

## Agent skills

- 读取、拆解或更新任务时，使用[本地 Markdown 任务约定](docs/agents/issue-tracker.md)。
- 使用工程类 Skill 时，按[领域文档约定](docs/agents/domain.md)读取相关术语和架构决策。
