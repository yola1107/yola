# Yola

Yola 是基于 Kratos v3 的长连接框架。Gateway 持有 TCP/WebSocket 连接，通过 Redis Locator 和 gRPC 将请求投递到 Node；业务通过 command handler 和 `node.Session` 绑定、续租、解绑及推送。

Gateway 与 Node 分别作为 Kratos App 的 transport 运行。应用管理 identity、配置、日志和外部依赖；业务管理玩家、Table 和执行模型。

项目尚未上线，按当前设计演进。改动同步仓内调用方、测试和文档，无需维护历史兼容层或数据迁移流程。

## 项目结构

```text
yola/
├── api/                 # 客户端与集群通信协议
├── gateway/             # 连接管理、认证与请求转发
├── node/                # 业务分发、会话与绑定
├── network/             # TCP / WebSocket 传输
├── event/               # 事件接口与 NATS 实现
├── locate/              # 绑定定位接口与 Redis 实现
├── registry/            # etcd 服务注册与发现
├── instance/            # 实例元数据
├── internal/            # 框架内部组件
├── examples/            # 最小应用与客户端示例
├── test/                # 游戏、集成测试与压测，独立 Go module
│   ├── gateway/         # 测试 Gateway
│   ├── ludo/            # Ludo 服务、客户端与压测
│   ├── whot/            # Whot 服务、客户端与压测
│   ├── internal/        # 测试模块共享组件
│   └── go.mod
├── docs/                # 接入、架构、性能与待办
│   └── agents/          # 工程 Skills 的任务与领域文档配置
├── AGENTS.md            # AI 开发约定
├── Makefile             # 生成、构建与验证入口
├── .golangci.yml        # Go 格式化与 lint 配置
├── buf.yaml             # Protobuf 检查配置
├── buf.gen.yaml         # Protobuf 生成配置
└── go.mod               # 框架 Go module
```

## 文档入口

| 要做什么 | 阅读入口 |
| --- | --- |
| 从零装配 Gate/Node，发出一次请求 | [最小接入流程](./README.md#最小接入流程) |
| 接入框架、理解路由与生命周期 | [架构契约](./architecture.md) |
| 发布事件、订阅及在线广播 | [EventBus 接入](./eventbus.md) |
| 复测性能、评估容量 | [性能验证](./performance.md) |
| 判断下一步要修什么、何时验收 | [当前待办](./issues.md) |
| 启动最小应用 | [examples](../examples/README.md) |
| 运行完整游戏、集成测试及压测 | [测试模块](../test/README.md)、[Ludo 压测入口](../test/ludo/README.md#当前-press-入口) |
| 使用工程类 Skills | [本地任务](./agents/issue-tracker.md)、[领域文档](./agents/domain.md) |

## 快速开始

要求 Go 1.26.6 或更高版本，以及 Redis、etcd、Core NATS。示例使用以下环境变量：

| 变量 | 默认值 / 用途 |
| --- | --- |
| `YOLA_REDIS_ADDR` | `127.0.0.1:6379` |
| `YOLA_REDIS_PASS` | 由运行环境注入；源码不得保存非空默认口令 |
| `YOLA_ETCD_ADDR` | `127.0.0.1:2379` |
| `YOLA_NATS_URL` | `nats://127.0.0.1:4222` |
| `YOLA_ADVERTISE_HOST` | `127.0.0.1`；跨主机时设为调用方可达地址 |

在仓库根目录打开独立终端，依次启动：

```powershell
go run ./examples/whot -id whot-1
go run ./examples/ludo -id ludo-1
go run ./examples/gateway -id gateway-1
go run ./examples/client -service whot
```

Whot 演示 Stateful 绑定，Ludo 演示 Stateless 路由；客户端可改为 `-service ludo`。完整装配、端口及停止回收见 [示例说明](../examples/README.md)。

## 最小接入流程

先启动 Node，再启动 Gateway，最后连接客户端。Gateway 负责认证，Node 注册业务 handler。

```text
Client(service=ludo, token)
  → Gate.Authenticator 返回 UID → Redis 保存 GateBinding
  → Request(EchoCommand) → Gate 按 service 发现 Node
  → Node 解码 protobuf、执行 handler → OpResponse → Client
```

### 1. 应用准备依赖

- Gateway 需要 Authenticator、Redis Locator 和 Registry/Discovery。
- Stateless Node 需要 Registry；Stateful Node 还需要 Locator。

每个进程创建自己的依赖实例，在 `runGate` / `runNode` 返回且组件停止后关闭。下面的 `reg` 是 `*etcd.Registry`，`store` 实现 `locate.Locator`。

```go
reg, err := etcd.New(etcd.WithEndpoints("127.0.0.1:2379"))
if err != nil {
	return err
}
defer reg.Close()

redisClient := redis.NewClient(&redis.Options{
	Addr: "127.0.0.1:6379", Password: os.Getenv("YOLA_REDIS_PASS"),
	ContextTimeoutEnabled: true,
})
defer redisClient.Close()
store := locateredis.New(redisClient)
```

这里 `etcd` 为 `yola/registry/etcd`，`locateredis` 为 `yola/locate/redis`，`redis` 为 `github.com/redis/go-redis/v9`。

业务实现 `gateway.Authenticator.Authenticate(ctx, serviceName, token, remoteIP) (string, error)`。它校验 service 和凭据，返回规范化 UID；拒绝时可返回 `gateway.ErrInvalidCredentials`。

下例通过 `auth` 注入认证器，参考[示例实现](../examples/gateway/main.go)。生产环境须验证 token 中的 UID。

### 2. Gateway 装配

以下启动函数使用标准包 `context`、`log/slog`、`time`，框架包 `yola/gateway`、`yola/locate`、`yola/network/tcp`、`yola/network/websocket`，以及 Kratos v3。

`9010` 是内部 gRPC 端口；客户端通过 TCP `3101` 或 WebSocket `3102` 接入。

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

这个 Stateless Node 处理 Echo，不保存玩家状态。service 名称须与客户端的 `ludo` 一致，command 须在 `BeforeStart` 前注册。

`message.EchoCommand` 来自 `yola/examples/message`，请求和响应使用 `wrapperspb.StringValue`。实际业务替换为自己的 protobuf 和 command 常量。

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

`wrapperspb` 为 `google.golang.org/protobuf/types/known/wrapperspb`。Node 必须使用就绪 Registrar。

Kratos 调度正常停止；`defer` 负责补充清理，包括 `Run` 启动失败的情况。清理使用独立预算，并保留原 `Run` 错误。

### 4. 客户端认证与请求

`tcp.NewClient` 等待认证完成，`token` 是业务认证器接受的凭据。返回的 `code` 是 gRPC 状态码，`0` 表示成功；业务内容从响应 body 解码。

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

WebSocket 使用 `websocket.NewClient` 和 `WithEndpoint("ws://127.0.0.1:3102")`，service、token 和请求协议相同。完整用法见 [client 示例](../examples/client/main.go)。

### 5. Stateful Node 的差异

Stateful Node 在上述装配中增加：

- 配置 `node.Locator(store)`，使用稳定且在线唯一的 ID，例如 `whot-1`。
- 将 `App.Name` 和客户端 service 设为 `whot`。
- 向 App 传入 `kratos.Metadata(server.Metadata())`；已有业务 metadata 须先合并。

保留原有的 `BeforeStart`、`Server` 和就绪 Registrar。

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

- 离桌时调用 `sess.UnbindNode(ctx)`；断线不会自动解绑 Node。
- 向本请求的连接推送用 `sess.Push`；按 UID 定位当前连接用 `server.PushToUID`。
- 绑定保活用 `sess.RenewNode(ctx)`，由业务在 6h 到期前调度。遇到 `Aborted` 时处理归属变化，不能退回 `BindNode` 覆盖绑定。
- 持有玩家、Table 或后台任务时，配置 `node.Drain(usecase.Drain)`。返回前停止绑定、续租和推送的生产者。

自定义 `locate.NodeLocator`、`node.Session` 和测试替身须实现条件续租。续租和原 ID 重启的边界见[修改权契约](./architecture.md#node-binding-修改权)。

装配见[完整示例](../examples/whot/main.go)，业务接入见 [Session handler](../examples/whot/service.go)。

以上地址用于本机接入。跨主机时配置可达的 `AdvertiseHost` / endpoint，TLS 两端成对启用。可选 EventBus 按[事件接线](./eventbus.md)装配；失败回收见[生命周期](./architecture.md#3-生命周期)。

## 开发

### 源码导航

Gateway/Node 按职责阅读：

| 链路 | 源码入口 |
| --- | --- |
| 认证与请求 | [Gateway 入站与认证](../gateway/inbound.go) → [转发](../gateway/forward.go) → [Node RPC 与分发](../node/dispatch.go) → [command 注册](../node/register.go) |
| 绑定、续租与推送 | [Session 绑定与上下文](../node/session.go)、[请求连接与 UID 推送](../node/push.go) |
| 启动、就绪与停止 | [Gateway 生命周期](../gateway/lifecycle.go)、[Node 生命周期与 Registrar](../node/lifecycle.go) |
| socket 接入与请求处理 | [TCP 连接](../network/tcp/server_connection.go)、[WebSocket 连接](../network/websocket/server_connection.go) |

选项声明与校验放在 `options.go` 或 `*_options.go`。跨职责的 pipeline、request lifecycle 和集成场景按契约组织。

### 测试约定

- 优先扩展就近的现有测试，仅补充未覆盖的行为或回归风险。新文件须有职责、构建或隔离需要。
- Bug 用例须复现原问题并在修复后通过；等价清理的补充用例须在清理前后均通过。
- 用例在前，文件内 helper 在后；包内共用夹具放在 `test_helpers_test.go`。
- 按契约断言；fake 只模拟所需行为，意外调用须显式失败。
- TTL、Redis 原子修改和 etcd 租约由 adapter 测试验证，示例测试验证装配参数。
- 资源创建成功后立即注册清理，再断言就绪；阻塞 worker 须有失败释放路径，并有界等待退出。

### 常用命令

以下命令在仓库根目录执行，定义见 [Makefile](../Makefile)。

| 命令 | 用途 |
| --- | --- |
| `make help` | 查看命令说明 |
| `make init` | 安装或更新开发工具 |
| `make api` | 生成根协议代码 |
| `make generate` | 运行代码生成并整理根 module 依赖 |
| `make build` | 构建根 module |
| `make check` | 检查根协议及两个 Go module |
| `make lint` | 对两个 Go module 执行 lint |
| `make race` | 对两个 Go module 执行竞态测试 |
| `make breaking 'BUF_BREAKING_AGAINST=.git#ref=<基线>'` | 检查根协议兼容性 |
| `make all` | 生成根代码并执行 `make check` |
| `make clean` | 清理根目录构建产物 |

单包测试在所属 module 执行 `go test <包路径>`。测试应用的命令见 [Ludo Makefile](../test/ludo/Makefile) 和 [Whot Makefile](../test/whot/Makefile)。
