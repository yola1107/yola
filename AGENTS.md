# AI 开发指南

Yola 是基于 Kratos 的长连接框架；根目录与 `test/` 是独立 Go module。

### 绝对红线

- **授权**：`git commit`、`git push` 和发布须明确授权；只处理当前任务，不覆盖或回退用户已有改动。
- **标识符**：未经任务要求，不改协议、配置、路由或用户指定标识符；跨文件及公共 API 改名须有任务授权，并核对引用、接口实现和兼容性。
- **生成文件**：不手改标有 `Code generated`、`auto-generated` 或 `DO NOT EDIT` 的文件；修改源定义后生成。
- **SQL**：值必须参数化。
- **Shell**：外部参数按业务白名单校验并通过参数列表传递，禁止拼接进 shell 命令。
- **TLS**：生产代码禁止 `InsecureSkipVerify: true`；仅负向测试允许。
- **凭据**：发现明文密钥、Token 或密码时报告位置与风险，不复制、扩散或擅自修改相关逻辑。
- **验证结果**：如实区分本次运行、有效复用和未运行的检查；未经验证不得声称通过。

### 范围与授权

- 评审、诊断和规划默认只读；实施完成修改、验证和复审，范围外问题只报告。
- Skill 不扩大授权；个人工具配置和 Skill 偏好不作为仓库执行前提。

### 项目资料

- 职责、路由、状态与生命周期：[架构契约](docs/architecture.md)。
- EventBus：[接入契约](docs/eventbus.md)。
- 性能优化：[性能基线](docs/performance.md)。
- 开发、生成与测试隔离：[开发文档](docs/README.md#开发与验证)。

### 代码约定

- 状态归属、依赖和生命周期遵循架构契约。 优先级： 安全 > 清晰度 > 简单 > 短
- 重构保持行为等价：求值次数与顺序、短路、锁范围、`defer`、关闭顺序、error identity、`nil`、零值、空集合及 I/O 时机均不变。
- 优先删除死代码、重复逻辑和无价值转交层；新增抽象须有真实职责、复用或测试价值。
- 注释用简洁中文解释契约、原因或所有权，Go doc 以声明名开头；命名沿用项目术语。
- import 默认使用包原名；别名仅用于包名冲突、路径末段与包名不一致，或区分 proto 域与版本。
- import 分组遵循 `.golangci.yml`：标准库、项目包、其他第三方包，组间空行。
- 函数和方法签名默认保持单行；超过 `.golangci.yml` 的行长限制时再换行。
- 缓存和性能优化须有依据；缓存明确所有者与失效条件，结论区分理论与实测。
- 新增生产依赖前评估标准库和现有依赖。新增缓存须有性能依据、所有者和失效条件；性能结论区分理论收益与实测结果。
- 功能修改同步调用方、测试和文档；按职责与迁移顺序分批，不交付空实现、无完成条件的 TODO 或临时双轨。

### 验证

- Go 格式化：仓库根执行 `golangci-lint fmt --config .golangci.yml <修改文件>`。
- Go 包测试（未触发 `make check` 时）：所属 module 执行 `go test <受影响包>`。
- Go lint：仓库根执行 `make lint`。
- 公共 API、依赖、跨 module 或多包改动：仓库根执行 `make check`，不重复其已覆盖的普通检查。
- 并发、连接或生命周期改动：所属 module 执行 `go test -race <受影响包>`。
- 根 `api/**` 协议改动：仓库根执行 `make breaking 'BUF_BREAKING_AGAINST=.git#ref=<基线>'`。
- 服务入口或构建链改动：在对应的仓库根、`test/ludo` 或 `test/whot` 执行 `make build`。
- 无 build 目标的入口改动：所属 module 执行 `go build -o <输出目录>/ <入口包>`。

- 全量或外部服务测试前确认 `TestMain`、环境与依赖；真实 Redis/etcd 测试遵守[隔离要求](docs/README.md#开发与验证)。
- 仅文档改动核对引用、命令和约束一致性。
- 仅复用代码、依赖、配置、工具及环境未变的有效结果。
- `make lint` 仅用于 AI 和本地检查，不接入 `make check`、`make all`、CI、Git hooks 或自动化 commit/push 门禁。
- 区分新增与存量 lint 告警，不通过关闭 linter、扩大 exclusion 或无关重构规避。

### 工具与生成

- `make init` 仅用于初始化或明确升级；常规验证用已安装工具。
- 按对应 Makefile 生成；根 `make all` 仅用于根 module 完整生成，会更新生成文件与依赖，不覆盖 `test` module。

### Git 与交付

- 改动前检查工作树；交付前复审完整任务 diff，执行 `git status --short`、`git diff --check`、`git diff --cached --check`。
- 提交获授权后，只暂存复审过的任务改动；每次提交一个原子改动，生成文件与源文件一起提交。格式：`<type>(<scope>): <中文摘要>`，type 使用 `feat|fix|refactor|docs|test|chore`。
- 用简洁中文交付结果、关键依据和验证情况；代码结论给出 `path:line`，说明未完成项及剩余风险。
