# Kratos 长连接示例

这些示例演示客户端如何通过 Gateway 访问 Node，以及业务如何绑定玩家、直接推送和广播消息。Ludo 演示 Stateless 路由，Whot 演示 Stateful 绑定；只运行一条 Echo 链路可按[快速开始](../docs/README.md#快速开始)操作。

## 环境配置

运行服务端前先启动 Redis、etcd 和 Core NATS。示例使用在线、允许丢失的 Pub/Sub，不启用 JetStream。环境变量在启动进程前设置：

| 环境变量 | 默认值 | 用途 |
| --- | --- | --- |
| `YOLA_REDIS_ADDR` | `127.0.0.1:6379` | Gateway 和 Whot 的 Redis Locator 地址 |
| `YOLA_REDIS_PASS` | 空 | Redis 口令，由运行环境注入 |
| `YOLA_ETCD_ADDR` | `127.0.0.1:2379` | Node 注册与发现使用的 etcd 地址 |
| `YOLA_NATS_URL` | `nats://127.0.0.1:4222` | 在线公告使用的 NATS 地址 |
| `YOLA_ADVERTISE_HOST` | `127.0.0.1` | 内部 gRPC 监听与公布地址；跨主机时设为调用方可达地址 |

这些默认配置用于本地测试。Redis 口令不得以非空默认值保存到源码；示例 token 也只用于本地链路测试。生产部署要求见[部署约束](../docs/project/architecture.md#7-部署与安全约束)和 [NATS 验收](../docs/project/eventbus.md#部署验收)。

## 启动与连接

服务端的默认身份和地址如下，`env.Host` 来自上面的公布地址配置：

| 应用 | Kratos Name | 默认 ID | gRPC 地址 |
| --- | --- | --- | --- |
| Whot | `whot` | `whot-1` | `<env.Host>:9001` |
| Ludo | `ludo` | `ludo-1` | `<env.Host>:9002` |
| Gateway | `gateway` | `gateway-1` | `<env.Host>:9010` |

Gateway 默认监听 TCP `127.0.0.1:3101` 和 WebSocket `127.0.0.1:3102`，分别通过 `-tcp-port`、`-ws-port` 调整；服务端 gRPC 端口使用 `-grpc-port` 调整。

在仓库根目录打开独立终端，依次启动：

```sh
go run ./examples/whot -id whot-1
go run ./examples/ludo -id ludo-1
go run ./examples/gateway -id gateway-1
```

Gateway 自动发现 Node。Client 在认证时通过 `-service` 选择业务，每个 Client 使用独立 UID；省略 `-uid` 时使用 `player-<pid>`。再打开三个终端运行以下客户端，可观察 Whot 推送与跨 service 公告：

```sh
go run ./examples/client -addr 127.0.0.1:3101 -service whot -uid player-1
go run ./examples/client -addr 127.0.0.1:3101 -service whot -uid player-2
go run ./examples/client -addr 127.0.0.1:3101 -service ludo -uid player-3
```

Client 启动后立即请求一次，此后每 3 秒请求一次，Echo 请求携带发送方 UID：

- Whot 依次执行 Enter、Echo、Leave，输出 `<instance-id> say hello`。发起者收到直接 Push，全部在线 Client 收到包含其 UID 的公告。
- Ludo 只执行 Echo，也能收到 Whot 发布的公告。
- 连接或请求失败后，Client 会在下个周期重新连接并认证，可用于观察服务下线和恢复。按 Ctrl+C 退出。

## 实例身份与重启

Gateway、Whot 和 Ludo 通过 `-id` 设置 Kratos instance ID。启动第二个同名实例时，同时指定不同的 ID 和端口：

```sh
go run ./examples/whot -id whot-2 -grpc-port 9003
```

Whot 在 Enter 时把玩家绑定到当前 Node ID，后续 Echo 和换 Gateway 重连仍路由到该实例，Leave 时解绑。重启同一逻辑实例须复用原 ID；新增实例使用新的稳定 ID。复用 ID 只恢复路由可达性，游戏状态仍由业务恢复。

生产环境应由配置或 StatefulSet ordinal 分配稳定 Whot ID，不能用 PID 作为重启后的身份。Gateway ID 只要求在线实例间唯一；Ludo 是无状态服务，使用普通负载均衡。

## 装配与资源管理

三个服务端共用 `etcd.NewNodeRegistry` 工厂，从同一环境 prefix 派生内部 Node 目录。应用把 Gateway 或 Node 传给 `kratos.Server`，由 Node 在 Start 内登记模式和唯一内部 endpoint，Gateway 只发现 Node。完整接入方法见[长连接接入](../docs/project/integration.md)。

Whot 显式配置 `node.Stateful`，handler 从 context 取得 `node.Session`，调用 `BindNode`、`UnbindNode` 和 `Push`。Handler 使用标准 `ctx + request -> response + error` 签名，不直接操作 Locator、Registry、NodeID 或 endpoint。`Session.Push` 使用当前请求的连接；只有按 UID 推送才需要配置 `node.GateDirectory`。

Whot 通过 NATS Bus 发布公告，Gateway 订阅同一 Topic，将 Payload 映射为客户端 `AnnouncementCommand`。普通 `ChanSubscribe` 使每个在线 Gateway 都收到一份，再由 Gateway 广播给已认证连接。示例公告队列为 256、Payload 上限为 64KiB；部署时按实际事件大小和突发量配置。

示例使用标准 `log/slog`，并向 Kratos 传入 service/app 属性；日志策略由外层应用配置。依赖回收与停机处理见 [Gateway](gateway/main.go)、[Whot](whot/main.go)和 [Ludo](ludo/main.go)的装配代码。
