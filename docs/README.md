# 长连接服务框架

本项目是基于 Kratos 的 Go 长连接服务框架，用于构建多人游戏、实时互动等需要持续双向通信的后端。客户端通过 TCP 或 WebSocket 连接 Gateway，由 Gateway 认证并转发请求，Node 执行业务命令并返回结果。

```mermaid
flowchart LR
    C[客户端] <-->|TCP / WebSocket| G[Gateway：连接、认证与路由]
    G <-->|内部 gRPC| N[Node：业务命令处理]
```

Gateway 和 Node 可以分别部署多个实例。框架提供以下能力：

- **请求路由**：无状态业务按负载均衡选择 Node；有状态业务可把玩家绑定到指定 Node，后续请求按绑定路由。
- **消息推送**：向当前连接回复或推送消息，按 UID 定位玩家推送，以及向 Gateway 在线连接广播。
- **应用生命周期**：Gateway 和 Node 作为 Kratos transport 接入 App，管理内部通信、登记、租约和停机排空。

Redis 保存连接定位与 Node 绑定，etcd 提供 Node 注册与发现；需要在线事件和跨 Gateway 广播时可接入 Core NATS。游戏规则、玩家和房间状态、事务及持久化由业务应用管理。

## 文档导航

| 任务 | 文档 |
| --- | --- |
| 理解组件职责、路由与生命周期 | [架构契约](project/architecture.md) |
| 业务接入与应用示例 | [接入指南](project/integration.md)、[示例说明](../examples/README.md) |
| 事件、订阅与在线广播 | [EventBus](project/eventbus.md) |
| 性能测试与容量评估 | [性能验证](project/performance.md) |
| 术语和架构决策 | [领域术语](../GLOSSARY.md)、[架构决策](adr/0001-self-contained-transports.md) |
| 编写、修改或审查 Go 代码 | [Go 编码规范与验收](../AGENTS.md#go-代码规范与验收) |
| 领域建模与本地任务约定 | [领域文档](agents/domain.md)、[任务约定](agents/issue-tracker.md) |
| 构建、检查与测试 | [开发与验证](#开发与验证) |
| 提交代码与调整提交流程 | [提交约定](../AGENTS.md#提交约定) |
| AI 开发流程与授权边界 | [AI 开发指南](../AGENTS.md) |

## 快速开始

准备 Go 1.26.6 或更高版本，并启动本机 Redis（`127.0.0.1:6379`）、etcd（`127.0.0.1:2379`）和 Core NATS（`nats://127.0.0.1:4222`）。以下示例使用这三个服务，默认按无密码 Redis 连接。

使用其他地址、Redis 口令或跨主机部署时，先按[示例环境配置](../examples/README.md#环境配置)设置环境变量。随后从仓库根目录打开三个终端，按顺序启动：

| 终端 | 命令 | 作用 |
| --- | --- | --- |
| 1 | `go run ./examples/ludo -id ludo-1` | 启动处理 Echo 命令的无状态 Node |
| 2 | `go run ./examples/gateway -id gateway-1` | 启动 Gateway，接收客户端连接并路由请求 |
| 3 | `go run ./examples/client -addr 127.0.0.1:3101 -service ludo -uid player-1` | 连接 Gateway，认证后向 Ludo 发出请求 |

客户端成功后会输出 `response received`，响应内容为 `ludo: player-1 hello`，之后每 3 秒请求一次。按 Ctrl+C 退出。Whot 示例进一步演示玩家绑定、粘性路由和消息推送，运行方法见[示例说明](../examples/README.md#启动与连接)。

## 业务接入

接入自己的业务时，从[最小接入流程](project/integration.md#最小接入流程)开始：准备认证器与外部依赖，装配 Gateway 和 Node，注册业务 command，再由客户端认证并发起请求。该流程使用无状态 Node，完整代码由可运行示例维护。

需要让玩家的后续请求固定到同一实例时，继续阅读 [Stateful Node 接入](project/integration.md#stateful-node-接入)，由业务决定绑定、续租和解绑时机。已有的 HTTP/gRPC 服务也可以通过 [Sender](project/integration.md#普通-kratos-服务推送)向在线玩家推送消息。

## 开发与验证

根目录与 `test/` 是独立 Go module：前者包含框架和最小示例，后者包含完整游戏、集成测试和压测。根目录的 `go test ./...` 不覆盖 `test/`。

项目尚未上线，按当前设计演进。改动应同步仓内调用方、测试和文档，无需维护历史兼容层或数据迁移流程。

### 常用命令

以下命令均从仓库根目录执行，定义见 [Makefile](../Makefile)。首次开发先用 `make init` 安装检查工具。`golangci-lint` 的版本在根目录 `Makefile` 中固定，`make fmt`、`make lint` 和 `make lint-fix` 会检查本机版本是否一致；格式规则包括 `gci` 和 `gofumpt`。

| 命令 | 功能与范围 |
| --- | --- |
| `make` / `make help` | 查看根 Makefile 的全部命令与用途 |
| `make init` | 安装 Buf、staticcheck 和仓库固定版本的 golangci-lint |
| `make api` | 生成根 module 的 API Protobuf 代码 |
| `make build` | 构建根 module 的全部包，不包含 `test/` |
| `make generate` | 运行根 module 的生成器并整理依赖，会更新相关文件 |
| `make check` | 检查根协议与依赖，对两个 module 运行静态分析和测试，检查 Git 空白 |
| `make fmt` | 校验工具版本，对两个 module 执行 `gci`、`gofumpt` 格式化，会修改代码 |
| `make lint` | 校验工具版本，对两个 module 运行 golangci-lint，不修改代码 |
| `make lint-fix` | 先执行 `make fmt`，再对两个 module 运行可自动修复的 lint 规则，会修改代码 |
| `make race` | 对两个 module 运行竞态检测测试 |
| `make breaking BUF_BREAKING_AGAINST='.git#ref=<tag-or-commit>'` | 检查根 API 相对指定版本的兼容性；替换为实际 tag 或 commit |
| `make all` | 依次执行 `api`、`generate` 和 `check`，会更新相关文件 |
| `make clean` | 删除根目录的 `bin/` 和 `*.exe` 构建产物 |
| `make -C test/ludo help` | 查看 Ludo 应用的生成、构建与测试命令 |
| `make -C test/whot help` | 查看 Whot 应用的生成、构建与测试命令 |

`make lint-fix` 只修复支持自动修复的问题，运行后检查 `git diff` 并执行 `make lint` 确认剩余问题。`make check` 不改写受版本控制的文件，也不包含 `make lint`、`make race` 或 `make breaking`；按改动影响选择附加检查。

### 测试入口

单包测试在所属 module 执行。例如，从仓库根目录运行：

```sh
go test ./gateway
(cd test && go test ./...)
```

测试编写遵循[设计与测试约定](../AGENTS.md#设计与测试约定)：TTL、Redis 原子修改和 etcd 租约由 adapter 测试验证，示例测试验证装配参数。

#### Redis 与 etcd 集成测试

真实 Redis/etcd 场景通过环境变量启用；未配置时相关测试会跳过，因此 `make check` 成功不能单独证明这些场景通过。

先准备专用、可丢弃的单机 Redis 和 etcd 实例，再从仓库根目录使用 sh、bash 或 zsh 执行。以下使用本机默认端口，其他地址替换对应变量值；Redis 需要认证时，通过运行环境设置 `YOLA_REDIS_PASSWORD`。

```sh
YOLA_REDIS_INTEGRATION=127.0.0.1:6379 \
YOLA_ETCD_INTEGRATION=127.0.0.1:2379 \
go test -race -count=1 -timeout=180s -v \
  ./locate/redis ./registry/etcd ./gateway ./node ./examples/env
```

该命令验证 Redis 绑定与 epoch、etcd 注册与租约、Gateway/Node 联动和示例装配，包含 Node 注册失租后的停止回收。`-count=1` 禁用测试结果缓存，`-v` 显示用例结果；确认目标场景为 `PASS`，并核对 `SKIP` 的原因。Redis Cluster 拓扑测试需要单独配置，入口见[集群测试](../locate/redis/cluster_test.go)。

#### 游戏集成测试与压测

`test/` module 的依赖、参数和运行命令见[测试模块](../test/README.md)及 [Ludo 压测](../test/ludo/README.md#当前-press-入口)。

## 维护边界

本文维护项目用途、唯一文档导航、运行入口和开发命令。装配方法与行为契约放在 `docs/project/`；AI 执行步骤和授权边界放在 `AGENTS.md`。

- 仅在本文维护文档导航，不重复维护完整目录树或源码导航。标题按阅读任务组织，同一主题的步骤、前提和注意事项放在一起。
- 操作说明写清依赖、配置、命令和预期结果；完整代码由可运行示例维护。详细实现链接源码，必要的运行配置直接说明。
- 移动文档或修改标题时，同步路径与锚点。Uber [原文](harness/rules/uber-go/style.md)和[许可证](harness/rules/uber-go/LICENSE)来自 [uber-go/guide@1d60a91](https://github.com/uber-go/guide/blob/1d60a91aa5e87d443002e23c21903c49489dbde5/style.md)，收录于 `2026-10-18`；保持原样，升级时从同一提交替换并更新来源。项目补充规则须明确范围，覆盖 Uber 条款时链接原文。
- 部署验收条件随功能文档维护，完成证据放在 `.scratch/<feature>/`；术语和架构决策分别维护在 `GLOSSARY.md` 与 `docs/adr/`，历史记录由 Git 追溯。
- 项目说明使用“本项目”或“框架”等通用称呼；代码和命令中的 API、路径、环境变量与实际实现一致。
