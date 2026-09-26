# Yola

Yola 是基于 Kratos v3 的长连接框架。Gateway 持有 TCP/WebSocket 连接，通过 Redis Locator 和 gRPC 将请求投递到 Node；业务通过 command handler 和 `node.Session` 绑定 Node、解绑及推送。

Gateway/Node 是外层原生 Kratos App 的内嵌 transport。应用拥有 identity、配置、日志和外部依赖，业务拥有玩家、Table 和执行模型。项目处于首版开发阶段，不承诺旧协议、存储布局或 API 的兼容。

## 文档入口

| 要做什么 | 阅读入口 |
| --- | --- |
| 从零装配 Gate/Node，发出一次请求 | [最小接入流程](#最小接入流程) |
| 接入框架、理解路由与生命周期 | [架构契约](./architecture.md) |
| 发布事件、订阅及在线广播 | [EventBus 接入](./eventbus.md) |
| 复测性能、评估容量 | [性能基线](./performance.md) |
| 查看未解决问题和验收缺口 | [当前问题](./issues.md) |
| 查看 Gateway/Node 文件职责与清理依据 | [文件职责审查](./gateway-node-file-review.md) |
| 启动最小应用 | [examples](../examples/README.md) |
| 运行完整游戏、集成测试及压测 | [test](../test/README.md)、[Ludo press](../test/ludo/README.md) |

已完成的清理、审核轮次和实施流水账由 Git 保存，不维护第二套待办。需要旧记录时可用 `git show 61faa25:docs/refactor-progress.md` 或 `git show 61faa25:docs/architecture-review.md` 查看。

## 快速开始

要求 Go 1.26.6 或更高版本，以及 Redis、etcd、Core NATS。示例使用以下环境变量：

| 变量 | 默认值 / 用途 |
| --- | --- |
| `YOLA_REDIS_ADDR` | `127.0.0.1:6379` |
| `YOLA_REDIS_PASS` | 由运行环境注入；源码不得保存非空默认口令 |
| `YOLA_ETCD_ADDR` | `127.0.0.1:2379` |
| `YOLA_NATS_URL` | `nats://127.0.0.1:4222` |
| `YOLA_ADVERTISE_HOST` | `127.0.0.1`；跨主机时设为调用方可达地址 |

在独立终端中依次启动：

```powershell
go run ./examples/whot -id whot-1
go run ./examples/ludo -id ludo-1
go run ./examples/gateway -id gateway-1
go run ./examples/client -service whot
```

Whot 演示 Stateful 绑定，Ludo 演示 Stateless 路由；客户端可改为 `-service ludo`。完整装配、端口及停止回收见 [示例说明](../examples/README.md)。

## 最小接入流程

Gate 与 Node 是两个独立进程，各自创建一个原生 Kratos App。先启动 Node，再启动 Gate，最后连接客户端；认证只由 Gate 执行，业务 handler 只注册到 Node。

```text
Client(service=ludo, token)
  → Gate.Authenticator 返回 UID → Redis 保存 GateBinding
  → Request(EchoCommand) → Gate 按 service 发现 Node
  → Node 解码 protobuf、执行 handler → OpResponse → Client
```

### 1. 应用准备依赖

Gateway 需要 Authenticator、Redis Locator、Registry/Discovery；Stateless Node 只需要 Registry，Stateful Node 再加 Locator。下面的 `reg` 是 `*etcd.Registry`，`store` 是 `locate.Locator`；每个进程分别创建自己的依赖实例，在 runGate/runNode 返回并完成组件 Stop 后关闭。

```go
reg, err := etcd.New(etcd.WithEndpoints("127.0.0.1:2379"))
if err != nil {
    return err
}
defer reg.Close()

redisClient := redis.NewClient(&redis.Options{
    Addr: "127.0.0.1:6379", Password: os.Getenv("YOLA_REDIS_PASS"),
})
defer redisClient.Close()
store := locateredis.New(redisClient)
```

这里 `etcd` 为 `yola/registry/etcd`，`locateredis` 为 `yola/locate/redis`，`redis` 为 `github.com/redis/go-redis/v9`。业务实现 `gateway.Authenticator.Authenticate(ctx, serviceName, token, remoteIP) (string, error)`：验证允许访问的 service 和凭据，返回规范化 UID；拒绝可返回 `gateway.ErrInvalidCredentials`。下例将实现作为 `auth` 注入，不能在生产直接信任 token 中的 UID。[示例认证实现](../examples/gateway/main.go)

### 2. Gateway 装配

以下为启动函数；标准包使用 context、log/slog、time，框架包为 yola/gateway、yola/locate、yola/network/tcp、yola/network/websocket，以及 Kratos v3。9010 是内部 gRPC 端口，3101/3102 才是客户端入口。

```go
func runGate(auth gateway.Authenticator, store locate.Locator, reg *etcd.Registry) error {
    gate, err := gateway.NewServer(
        gateway.Address("127.0.0.1:9010"),
        gateway.Auth(auth),
        gateway.Locator(store),
        gateway.Discovery(reg),
        gateway.Transport(
            tcp.NewServer(tcp.Address("127.0.0.1:3101")),
            websocket.NewServer(websocket.Address("127.0.0.1:3102")),
        ),
    )
    if err != nil {
        return err
    }
    appCtx, cancelApp := context.WithCancel(context.Background())
    defer func() {
        cancelApp()
        cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
        defer cancel()
        if err := gate.Stop(cleanupCtx); err != nil {
            slog.Error("stop Gateway", "error", err)
        }
    }()
    app := kratos.New(
        kratos.Context(appCtx),
        kratos.ID("gateway-1"), kratos.Name("gateway"),
        kratos.StopTimeout(10*time.Second),
        kratos.BeforeStart(gate.BeforeStart),
        kratos.Server(gate),
        kratos.Registrar(reg),
    )
    return app.Run()
}
```

### 3. Node 注册业务并启动

这个最小 Stateless Node 只处理 Echo，不保存玩家状态；service 名称必须与客户端传入的 `ludo` 一致。`message.EchoCommand` 来自 `yola/examples/message`，请求/响应使用 `wrapperspb.StringValue`；实际业务换成自己的 protobuf 和 command 常量。注册必须在 BeforeStart 前完成。

```go
func runNode(reg *etcd.Registry) error {
    server, err := node.NewServer(node.Address("127.0.0.1:9002"))
    if err != nil {
        return err
    }
    node.Register(server, message.EchoCommand,
        func(_ context.Context, in *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
            return wrapperspb.String("hello " + in.Value), nil
        })
    appCtx, cancelApp := context.WithCancel(context.Background())
    defer func() {
        cancelApp()
        cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
        defer cancel()
        if err := server.Stop(cleanupCtx); err != nil {
            slog.Error("stop Node", "error", err)
        }
    }()
    app := kratos.New(
        kratos.Context(appCtx),
        kratos.ID("ludo-1"), kratos.Name("ludo"),
        kratos.StopTimeout(10*time.Second),
        kratos.BeforeStart(server.BeforeStart),
        kratos.Server(server),
        kratos.Registrar(server.Registrar(reg)),
    )
    return app.Run()
}
```

`wrapperspb` 为 `google.golang.org/protobuf/types/known/wrapperspb`。Node 的就绪 Registrar 不能省略；正常停止由 Kratos 调度，defer 还负责 Run 启动失败后的资源回收。它使用独立停止预算，并保留原 Run 错误。

### 4. 客户端认证与请求

使用 TCP Client；NewClient 会等待认证，`token` 是业务认证器接受的凭据。这里 `code` 是 gRPC 状态码，成功为0，业务内容在解码后的响应中。

```go
client, err := tcp.NewClient(ctx,
    tcp.WithAddress("127.0.0.1:3101"),
    tcp.WithServiceName("ludo"), tcp.WithToken(token),
)
if err != nil {
    return err
}
defer client.Close()
body, code, err := client.Request(ctx, message.EchoCommand, wrapperspb.String("Yola"))
if err != nil {
    return err
}
if code != 0 {
    return fmt.Errorf("Echo failed: code=%d", code)
}
reply := new(wrapperspb.StringValue)
if err := proto.Unmarshal(body, reply); err != nil {
    return err
}
fmt.Println(reply.Value)
```

WebSocket 使用 `websocket.NewClient`、`WithEndpoint("ws://127.0.0.1:3102")`，其余 service/token/request 对应相同协议；完整两种客户端见 [client 示例](../examples/client/main.go)。

### 5. Stateful Node 的差异

Stateful Node 创建时增加 `node.Locator(store)`，用稳定且在线唯一的 ID，例如 `whot-1`；App.Name 与客户端 service 改为 `whot`，并向 App 传入 `kratos.Metadata(server.Metadata())`。仍使用上面的 BeforeStart、Server 和就绪 Registrar。已有业务 metadata 须先合并再设置。

在入座等明确业务时点从请求 context 取 Session，再绑定本 Node；后续请求就会按 NodeID 精确路由：

```go
node.Register(server, message.WhotEnterCommand,
    func(ctx context.Context, _ *emptypb.Empty) (*emptypb.Empty, error) {
        sess, ok := node.FromContext(ctx)
        if !ok {
            return nil, errors.New("authenticated session is missing")
        }
        if err := sess.BindNode(ctx); err != nil {
            return nil, err
        }
        return new(emptypb.Empty), nil
    })
```

离桌由业务调用 `sess.UnbindNode(ctx)`；向本请求的连接推送用 `sess.Push(ctx, command, protobufMessage)`，按 UID 定位当前连接用 `server.PushToUID`。断线不自动解绑 Node，BindNode 也不能作为安全定时保活。持有玩家/Table/后台任务的业务增加 `node.Drain(usecase.Drain)`，返回前停止绑定和推送的生产者。[完整 Stateful 示例](../examples/whot/main.go)、[Session handler](../examples/whot/service.go)

上述回环地址用于本机接入；跨主机需显式配置可达的 AdvertiseHost/endpoint，TLS两端成对配置。EventBus是可选的应用依赖，按 [事件接线](./eventbus.md) 单独装配；资源归属和失败回收见 [生命周期](./architecture.md#3-生命周期)。

## 源码导航

读主链路时按 [Gateway 入站](../gateway/inbound.go) → [认证](../gateway/auth.go) / [转发](../gateway/forward.go) → [Node 分发](../node/dispatch.go) / [注册](../node/register.go) → [Session](../node/session.go) / [Push](../node/push.go) 阅读，再查看各包 lifecycle.go。

## 开发与验证

根目录与 `test/` 是独立 Go module；验证矩阵以 [AGENTS.md](../AGENTS.md#验证) 为准。

| 命令（仓库根目录） | 用途与影响 |
| --- | --- |
| `make check` | 根协议 buf lint、两个 module 的依赖检查、vet、staticcheck、测试及工作树/暂存区 diff 检查；不改写 tracked files |
| `make lint` | 两个 module 的本地/AI lint；不接入 check、all、CI、Git hooks 或提交门禁 |
| `make build` | 构建根 module；Ludo/Whot 在各自目录执行，测试 Gateway 在 test 目录执行 `go build -o ../bin/ ./gateway` |
| `make api` / `make generate` | 分别生成根协议；运行根 go generate 和 go mod tidy，会修改生成文件或依赖 |
| `make all` | 根完整生成后运行 check，不覆盖 test module |
| `make init` | 安装或更新工具，仅用于初始化或明确升级 |

Go 文件按 `golangci-lint fmt --config .golangci.yml <修改文件>` 格式化。根协议变更显式执行 `make breaking 'BUF_BREAKING_AGAINST=.git#ref=<基线>'`。test module 的生成按 [Ludo Makefile](../test/ludo/Makefile)、[Whot Makefile](../test/whot/Makefile) 选择 api/config/generate；它们的 all 只生成，不替代验证。

真实依赖测试前检查 TestMain、环境变量和资源归属，仅使用任务专用实例、DB/prefix 和可丢弃 UID，不改 tracked 配置、不操作既有服务数据；结束后核对并清理本任务资源。Redis/etcd 通过 `YOLA_REDIS_INTEGRATION`、`YOLA_ETCD_INTEGRATION` 注入；Redis 凭据使用 `YOLA_REDIS_PASSWORD`，NATS 外部测试使用 `YOLA_NATS_URL`。未配置时被跳过的测试不算通过。

Cluster 普通回归使用 `YOLA_REDIS_CLUSTER_INTEGRATION` 注入逗号分隔地址。`TestNodeBindingClusterMigration` 和 `TestNodeBindingClusterCooperativeFailover` 还要求 `YOLA_REDIS_CLUSTER_ADMIN_INTEGRATION=1`，地址包含专用 3 主 3 从全部六个节点；它们会修改 slot/角色，Failover 要求全库为空。每个管理场景使用新建集群，连续切换的稳定性仍有 [验证限制](./issues.md#cluster-tests)。
