# 长连接接入

本文从一个 Stateless Node 开始，说明如何准备依赖、装配 Gateway、注册业务命令并由客户端发起请求。运行环境按[快速开始](../README.md#快速开始)准备，组件职责与生命周期约束见[架构契约](architecture.md)。

完成最小流程后，有状态业务继续阅读 [Stateful Node 接入](#stateful-node-接入)；已有 HTTP/gRPC 服务需要通知在线玩家时，参见[普通 Kratos 服务推送](#普通-kratos-服务推送)。

## 最小接入流程

先启动 Node，再启动 Gateway，最后连接客户端。Gateway 负责认证，Node 注册业务 handler。

```text
Client(service=ludo, token)
  → Gate.Authenticator 返回 UID → Redis 保存 GateBinding
  → Request(EchoCommand) → Gate 按 service 发现 Node
  → Node 解码 protobuf、执行 handler → OpResponse → Client
```

文中地址用于本机接入。跨主机时配置可达的 `AdvertiseHost` / endpoint，TLS 两端成对启用。可选 EventBus 按[事件接线](eventbus.md)装配。

### 1. 应用准备依赖

| 组件 | 必需依赖 | 按需配置 |
| --- | --- | --- |
| Gateway | Authenticator、Locator、Node Discovery | TCP / WebSocket transport |
| Stateless Node | Node Registration | 按 UID 推送所需的 GateDirectory |
| Stateful Node | Node Registration、Node Locator | GateDirectory、业务 Drain |

应用创建并回收外部依赖。Gateway 和 Node 使用 `registry/etcd.NewNodeRegistry`，同一环境采用相同 `WithPrefix`；Redis client 须启用 `ContextTimeoutEnabled` 并设置有限的读写 timeout。完整构造见 [Gateway](../../examples/gateway/main.go)和 [Stateful Node](../../examples/whot/main.go)。

业务实现 `gateway.Authenticator.Authenticate(ctx, serviceName, token, remoteIP) (string, error)`。它校验 service 和凭据，返回规范化 UID；拒绝时可返回 `gateway.ErrInvalidCredentials`。

以下片段使用已构造的 `auth`、`store` 和 `reg`。它们分别是业务认证器、Locator 和 Node Registry；生产认证器须验证 token 中的 UID。

### 2. Gateway 装配

`9010` 是内部 gRPC 端口；客户端通过 TCP `3101` 或 WebSocket `3102` 接入。

```go
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
```

通过 `kratos.Server(gate)` 装入 App，并设置 `kratos.ID` 与 `kratos.Name`。完整运行与失败回收见 [Gateway 示例](../../examples/gateway/main.go)。

### 3. Node 注册业务并启动

这个 Stateless Node 处理 Echo，不保存玩家状态。service 名称须与客户端的 `ludo` 一致，command 须在 `App.Run` 前注册。

`message.EchoCommand` 来自[示例协议](../../examples/message/wire.go)，请求和响应使用 `wrapperspb.StringValue`。实际业务替换为自己的 protobuf 和 command 常量。

```go
server, err := node.NewServer(
	node.Address("127.0.0.1:9002"),
	node.Registration(reg),
)
if err != nil {
	return err
}
node.Register(server, message.EchoCommand,
	func(_ context.Context, in *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
		return wrapperspb.String("hello " + in.Value), nil
	})
```

`wrapperspb` 为 `google.golang.org/protobuf/types/known/wrapperspb`。通过 `kratos.Server(server)` 装入 App，设置实例 ID 和 `kratos.Name("ludo")`。Node 自己登记唯一内部 endpoint，无需配套 hook 或 App Registrar。完整装配见 [Stateless Node 示例](../../examples/ludo/main.go)。

应用须用 Wire/defer 处理 `Run` 早退，清理顺序见[应用装配](architecture.md#41-应用装配)。

### 4. 客户端认证与请求

`tcp.NewClient` 等待认证完成，`token` 是业务认证器接受的凭据。返回的 `code` 是 gRPC 状态码，`0` 表示成功；业务内容从响应 body 解码。

```go
client, err := tcp.NewClient(ctx,
	tcp.WithAddress("127.0.0.1:3101"),
	tcp.WithServiceName("ludo"),
	tcp.WithToken(token),
)
if err != nil {
	return err
}
defer client.Close()
body, code, err := client.Request(ctx, message.EchoCommand, wrapperspb.String("hello"))
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

WebSocket 使用 `websocket.NewClient` 和 `WithEndpoint("ws://127.0.0.1:3102")`，service、token 和请求协议相同。完整用法见 [client 示例](../../examples/client/main.go)。

## Stateful Node 接入

Stateful Node 在上述装配中增加：

- 配置 `node.Stateful(store)`，使用稳定且在线唯一的 ID，例如 `whot-1`。
- 将 `App.Name` 和客户端 service 设为 `whot`。
- 需要 `server.PushToUID` 时再配置 `node.GateDirectory(store)`；与 Stateful 模式独立。

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
- 持有玩家、Table 或后台任务时，配置 `node.Drain(usecase.Drain)`。返回前停止绑定、续租和推送的生产者。

装配见[完整示例](../../examples/whot/main.go)，业务接入见 [Session handler](../../examples/whot/service.go)。

### 续租与会话代次

绑定保活用 `sess.RenewNode(ctx)`，由业务在 6h 到期前调度。`Aborted` 表示绑定缺失或归属变化，不能退回 `BindNode` 覆盖绑定。

业务自行维护本地会话代次，每次更新或清除玩家会话时递增。`BindingToken` 标识物理连接；同一连接再次登录时，它可以保持不变，但业务会话代次仍须更新。实现可参考[玩家会话管理](../../test/ludo/internal/biz/player/player.go)。

- 续租前同时保存 Session 和本地代次。收到 `Aborted` 后，在管理玩家状态的桌内 mailbox 等执行单元中核对代次；忽略旧代结果，只有当前代失权时才停止该代的续租和推送。
- 已失权玩家只回收本地驻留，不保存旧状态或修改新归属的绑定。
- 重连的绑定与会话发布须和失权处理串行；重新绑定并重入成功后，才恢复此前失权的驻留。
- `Unavailable`、`DeadlineExceeded` 等暂时错误保留驻留，由后续调度重试。

自定义 Locator 或 Session 须遵守[修改权契约](architecture.md#node-binding-修改权)。

## 普通 Kratos 服务推送

普通 gRPC/HTTP 服务使用标准 Kratos Registrar，按同一环境 prefix 调用 `etcd.New`。与 Node 目录的隔离规则见 [Registry](architecture.md#45-registry)。

这类服务无需装配 Node。需要通知在线玩家时使用 [push 包](../../push/sender.go)：

```go
sender, err := push.NewSender(store)
if err != nil {
	return err
}
defer sender.Close()
return sender.PushToUID(ctx, "whot", uid, command, message)
```

目标游戏 `whot` 是玩家绑定的命名空间，与调用服务自身的 App.Name 无关。推送与重连语义见 [Push 契约](architecture.md#23-pushkick-与-disconnect)。
