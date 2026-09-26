# Yola

Yola 是基于 Kratos v3 的分布式长连接接入框架。Gateway 持有 TCP/WebSocket 连接，通过 Redis Locator 定位玩家连接和有状态 Node，并通过 gRPC 将请求负载均衡或精确投递到 Node。业务只实现 Kratos 风格的 command handler，通过 request-scoped `node.Session` 绑定 Node、解绑 Node 和 Push，不接触 Redis、Registry 或内部集群协议。

项目仍处于首版开发阶段，没有存量协议、数据或公开 API 的兼容承诺。当前实现优先保持单向、直接、可追踪的数据流，不为假设中的旧版本增加 fallback、feature gate 或重复协议。

第一轮行为等价清理已提交为 `ab0479b`，三轮审查归档于 `a9a0cf6`。I56–I64 九项 P1 已在 `9ca9ad7` 基线上实施并验证，生产净减 54 行、测试净减 130 行，保留 gate/node 的 Kratos v3 组件定位，随本次清理提交归档。低收益、高风险方向不进入执行队列，真实限制单独保留。

## 文档

- [架构设计](./architecture.md)：组件边界、网络与存储、生命周期、请求链路、粘性路由、默认参数与超时职责。
- [EventBus 接入](./eventbus.md)：Gateway/Node 在线实时 Pub/Sub、NATS 生命周期和 Gateway 有界并行 fanout。
- [框架去复杂审查](./architecture-review.md)：三轮审核的规模、方案、Kratos 边界、测试价值和静态估算；当前实际结果见 [实施记录](./refactor-progress.md#cleanup-results)。
- [框架优化执行清单](./issues.md)：I56–I64 的完成状态与当前依据；已知限制、P2 和 Drop 方向不作为本次待执行任务。
- [框架清理进度](./refactor-progress.md)：本次实施、三轮审核与第一轮清理分别记录；[原交接提示词](./refactor-progress.md#cleanup-handoff) 已执行，旧修复证据仅供追溯。
- [Node binding 修改权](./node-binding-fencing.md)：已实现的 service 原子分区与边界；删除未选定的协调和分片草案。
- [性能基线](./performance.md)：历史测量、可复现 benchmark 和容量验收口径，不作为本轮优化排期。
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
4. `node/register.go` → `node/dispatch.go` → `node/session.go`：handler 注册、fencing、request-scoped Session
5. `node/push.go` → `gateway/cluster.go`：Push 回程与目标路由校验
6. `gateway/lifecycle.go` / `node/lifecycle.go`：BeforeStart / Start / Stop（读完热路径后再看）

包职责与不变量见 [架构设计](./architecture.md)；问题及关闭记录集中在 [问题清单](./issues.md)，当前实施状态与交接见 [进度表](./refactor-progress.md)，历史细节由 Git 保留。

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

Gate/Node 是嵌入外层原生 Kratos App 的 transport 组件，内部 gRPC Server 不构成另一套 App。同进程业务通过接口注入或 handler 注册直接调用；跨进程业务使用普通 Kratos gRPC。应用身份、配置来源、完整 transport 列表及应用级资源回收仍由外层拥有，详见 [接入与服务边界](./architecture.md#11-接入与服务边界)。

Gateway App 配置依赖和客户端 Transport 后，直接把 `gateway.Server` 交给 Kratos。下面展示装配参数，完整资源回收见 [Gateway 入口](../examples/gateway/main.go) 和 [应用装配契约](./architecture.md#31-应用装配)：

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

appCtx, cancelApp := context.WithCancel(context.Background())
defer cancelApp()
app := kratos.New(
    kratos.Context(appCtx),
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

appCtx, cancelApp := context.WithCancel(context.Background())
defer cancelApp()
app := kratos.New(
    kratos.Context(appCtx),
    kratos.ID("whot-1"),
    kratos.Name("whot"),
    kratos.Metadata(server.Metadata()),
    kratos.StopTimeout(10*time.Second),
    kratos.BeforeStart(server.BeforeStart),
    kratos.Server(server),
    kratos.Registrar(server.Registrar(registry)),
)
```

Stateless Node 不配置 Locator，`Metadata()` 为 nil；Stateful Node instance ID 必须稳定且在线唯一。入口在 `Run` 返回后取消 App context，以独立、有界的 context 停止 Server，再关闭外部依赖；完整代码见 [Whot 入口](../examples/whot/main.go)。持有 Table、玩家或后台任务的 Node 通过 `node.Drain` 注入业务关闭：Drain 期间仍可绑定、解绑和推送，返回前必须停止这些操作的生产者。启动核验、租约失效和排空契约见 [生命周期](./architecture.md#3-生命周期)。

同 App 可由外层一次配置 `kratos.Server(server, httpServer)` 等完整列表；业务 metadata 与 `server.Metadata()` 也应先合并再设置，避免原生覆盖语义丢失配置。Kratos 并行停止各 transport，复用 usecase 的入口须由业务 owner 统一准入和排空。附加普通 gRPC 可同进程启动，但普通业务 RPC 须另定直连或发现方案，Gateway 不会在同一 service 记录中自动区分它与 Node 内部 RPC。显式 `App.Stop()` 的注销失败还需应用主动取消与清理，详见 [应用装配](./architecture.md#31-应用装配)。

示例和 `test` 共用根模块的 `yola/registry/etcd`：`New(WithEndpoints(...), WithPrefix(...))` 创建 Registry，调用方负责 `Close()`。它复用官方 Discovery/Watch 和 etcd Session，只在空 key 上登记，并仅撤销本次 lease。旧注册尚未回收时，同 service/ID 返回 `ErrInstanceExists`；需等待旧 lease 失效后重建应用，默认 TTL 为 15s。Node 使用 `server.Registrar(registry)` 适配 Kratos v3.0.0 的就绪时序。

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

I48 的 Cluster 回归使用 `YOLA_REDIS_CLUSTER_INTEGRATION` 注入逗号分隔的地址；凭据通过 `YOLA_REDIS_PASSWORD` 注入。普通绑定回归只操作随机 service 的 key。`TestNodeBindingClusterMigration` 与 `TestNodeBindingClusterCooperativeFailover` 还须显式设置 `YOLA_REDIS_CLUSTER_ADMIN_INTEGRATION=1`，且地址包含专用 3 主 3 从的全部 6 个节点；它们会修改 slot/角色，不能连接共享或业务集群。Failover 要求全库为空；建议每个管理场景使用新建集群，连续快速切换的组合稳定性尚未通过，详见 [I48 验证](./refactor-progress.md#i48-results)。
