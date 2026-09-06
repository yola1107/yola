# Yola

Yola 是基于 Kratos 的分布式长连接接入框架。Gateway 持有 TCP/WebSocket 连接，通过 Redis Locator 定位玩家连接和有状态 Node，并通过 gRPC 将请求负载均衡或精确投递到 Node。业务只实现 Kratos 风格的 command handler，通过 request-scoped `node.Session` 绑定 Node、解绑 Node 和 Push，不接触 Redis、Registry 或内部集群协议。

项目仍处于首版开发阶段，没有存量协议、数据或公开 API 的兼容承诺。当前实现优先保持单向、直接、可追踪的数据流，不为假设中的旧版本增加 fallback、feature gate 或重复协议。

历史依赖方向调整相对基线 `d01450f428bc` 包含有意的 breaking change：`gateway.RedisLocator`、`node.RedisLocator` 改为 `Locator(locateredis.New(client))`；`clusterv1.GateRouteFromBinding`、`GateBindingFromRoute` 已删除，跨层调用方应在自身边界显式转换所需字段，不在 proto 包重新引入 locator 依赖。

## 文档

- [架构设计](./architecture.md)：组件边界、网络与存储、生命周期、请求链路、粘性路由和默认参数。
- [Gateway/Node 架构复审](./gateway-node-review.md)：2026-09-06 框架简化结果、状态所有权、启动回滚实证及后续重构方案；区分已实施与候选设计。
- [Gateway/Node 重构实施](./gateway-node-refactor.md)：Node 租约所有权、原生 Kratos 装配边界及本轮验证记录。
- [EventBus 接入](./eventbus.md)：Gateway/Node 在线实时 Pub/Sub、NATS 生命周期和 Gateway 有界并行 fanout。
- [当前限制](./issues.md)：未关闭的部署约束及待验证、待设计事项。
- [根模块代码审查](./code-review.md)：最近一次审查记录与持续职责边界；历史验证不代表当前通过。
- [性能基线](./performance.md)：当前热路径成本、诊断优先级、可复现 benchmark 和容量验收口径。
- [示例说明](../examples/README.md)：Gateway、Stateful Whot、Stateless Ludo 和 Client 的本地运行方式。
- [测试模块](../test/README.md)：测试服务的配置与启动；Ludo 压测见 [Ludo README](../test/ludo/README.md)。

## 架构概览

```text
                         etcd Registry
                              │
Client ── TCP/WebSocket ── Gateway ── gRPC ── Node
                              │          │       │
                              │          └ Push ─┘
                              └──── Redis Locator ─┘
```

Gateway 保存物理 Session。Locator 保存带租约的 `(service, UID) -> GateBinding`、业务控制生命周期的 `(service, UID) -> NodeID`，以及 Stateful Node 的进程 epoch。Registry 提供 service、instance ID、gRPC endpoint 和 `sticky` metadata。Node 回程使用 `GateBinding` 中的 Gateway endpoint 直连，不 discovery Gateway service。

## 读代码顺序

按主链路阅读，避免在生命周期状态机与传输细节之间来回跳：

1. `gateway/inbound.go`：连接打开、帧分发、关闭清理
2. `gateway/auth.go` → `gateway/takeover.go`：认证、BindGate、旧连接 Kick
3. `gateway/forward.go` → `gateway/backend.go` / `gateway/balancer.go` / `gateway/resolver.go`：路由、粘性定位、负载均衡、服务发现与 gRPC Forward
4. `node/dispatch.go` → `node/session.go`：fencing、handler、request-scoped Session
5. `node/push.go` → `gateway/cluster.go`：Push/Kick 回程
6. `gateway/lifecycle.go` / `node/lifecycle.go`：BeforeStart / Start / Stop（读完热路径后再看）

包职责与不变量见 [架构设计](./architecture.md) 和 [根模块代码审查](./code-review.md)。

## 快速开始

要求 Go 1.26.6 或更高版本、Redis、etcd 和 Core NATS。示例读取以下环境变量：

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `YOLA_REDIS_ADDR` | `127.0.0.1:6379` | Redis 地址 |
| `YOLA_REDIS_PASS` | 由运行环境注入 | Redis 密码；tracked source 不得保存非空默认值 |
| `YOLA_ETCD_ADDR` | `127.0.0.1:2379` | etcd 地址 |
| `YOLA_NATS_URL` | `nats://127.0.0.1:4222` | Core NATS 地址 |
| `YOLA_ADVERTISE_HOST` | `127.0.0.1` | Registry 中发布的可达地址 |

依次启动服务：

```powershell
go run ./examples/whot -id whot-1
go run ./examples/ludo -id ludo-1
go run ./examples/gateway -id gateway-1

go run ./examples/client -service whot
go run ./examples/client -service ludo
```

示例 Whot 是 Stateful service，进入业务后绑定玩家 Node；示例 Ludo 是 Stateless service，始终使用普通负载均衡。端口、多实例和运行方式见 [示例说明](../examples/README.md)。

## 接入

Gateway App 配置依赖和客户端 Transport 后，直接把 `gateway.Server` 交给 Kratos：

```go
gate, err := gateway.NewServer(
    gateway.Address(grpcAddr),
    gateway.AdvertiseHost(advertiseHost),
    gateway.Auth(authenticator),
    gateway.Locator(locateredis.New(redisClient)),
    gateway.Discovery(discovery),
    gateway.Transport(
        tcp.NewServer(tcp.Address(tcpAddr), tcp.AdvertiseHost(advertiseHost)),
        websocket.NewServer(websocket.Address(wsAddr), websocket.AdvertiseHost(advertiseHost)),
    ),
)
if err != nil {
    return nil, err
}

app := kratos.New(
    kratos.ID("gateway-1"),
    kratos.Name("gateway"),
    kratos.StopTimeout(10*time.Second),
    kratos.BeforeStart(gate.BeforeStart),
    kratos.Server(gate),
    kratos.Registrar(registry),
)
```

Node App 注册业务 service 后，把 `node.Server` 作为 Kratos transport；Stateful Node 额外配置 Locator 并发布 `sticky` metadata：

```go
server, err := node.NewServer(
    node.Address(grpcAddr),
    node.AdvertiseHost(advertiseHost),
    node.Locator(locateredis.New(redisClient)),
    node.Drain(usecase.Drain),
)
if err != nil {
    return nil, err
}
v1.RegisterGameServer(server, gameService)

app := kratos.New(
    kratos.ID("whot-1"),
    kratos.Name("whot"),
    kratos.Metadata(server.Metadata()),
    kratos.StopTimeout(10*time.Second),
    kratos.BeforeStart(server.BeforeStart),
    kratos.Server(server),
    kratos.Registrar(registry),
)
```

Stateless Node 不配置 Locator，`Metadata()` 为 nil。`BeforeStart` 完成 identity、Locator 检查和按需 epoch 注册，`Start` 开放 gRPC 服务并按需启动 epoch 续租。EventBus 由应用组装层构造并关闭；持有 Table、玩家或后台任务的 Node 通过 `node.Drain` 注入业务关闭。Stateful Node instance ID 必须稳定且在线唯一。完整约束见 [架构设计](./architecture.md)。

## 开发与验证

从仓库根目录按用途选择命令；AI 的任务范围见 [范围与授权](../AGENTS.md#范围与授权)，必需检查见 [验证](../AGENTS.md#验证)。

| 命令 | 用途与影响 |
| --- | --- |
| `make lint` | 使用 `.golangci.yml` 检查两个 module，仅用于 AI 和本地质量检查，不接入 `make check`、`make all`、CI、Git hooks 或自动化 commit/push 门禁 |
| `make check` | 执行根协议 buf lint、两个 module 的依赖检查、vet、staticcheck、测试及工作树和暂存区的 diff 检查，不改写 tracked files |
| `make build` | 构建根 module；Ludo/Whot 在各自目录运行，测试 Gateway 在 `test` 目录用 `go build -o ../bin/ ./gateway` |
| `make init` | 通过 `go install ...@latest` 安装或更新用户环境中的开发工具，仅用于初始化或明确升级 |
| `make all` | 生成根 API，在根 module 执行 `go generate` 和 `go mod tidy` 后运行 `make check`，会改写根 module 的生成文件和依赖文件 |

根目录生成不覆盖独立 `test` module。[Ludo](../test/ludo/Makefile) 和 [Whot](../test/whot/Makefile) 按源定义选择各自目录的 `make api`、`make config` 或 `make generate`；需要完整生成时使用各自的 `make all`。子目录的 `all` 只生成 API、配置和 Wire 文件，不执行检查；生成后仍须完成适用验证。

协议变更需显式指定比较目标，不从默认检查隐式访问远端。PowerShell 示例：

```powershell
$env:BUF_BREAKING_AGAINST = '.git#ref=<tag-or-commit>'
make breaking
```

真实 Redis/etcd 集成测试只允许使用专用实例、专用 DB/prefix 和可丢弃 UID；地址通过 `YOLA_REDIS_INTEGRATION`、`YOLA_ETCD_INTEGRATION` 注入，不修改 tracked 配置。生产部署前必须完成 [当前限制](./issues.md) 中的安全与容量验收。
