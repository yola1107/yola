# Kratos 多游戏集群示例

| 应用 | Kratos Name | 默认 ID | gRPC 地址 |
| --- | --- | --- | --- |
| Whot | `whot` | `whot-1` | `<env.Host>:9001` |
| Ludo | `ludo` | `ludo-1` | `<env.Host>:9002` |
| Gateway | `gateway` | `gateway-1` | `<env.Host>:9010` |

Gateway 默认监听 TCP `127.0.0.1:3101` 和 WebSocket `127.0.0.1:3102`，可分别通过 `-tcp-port`、`-ws-port` 调整；gRPC 使用 `-grpc-port`。Gateway 通过 Registry 自动发现游戏服务。Client 在 Auth 中指定 `service_name`；Gateway 使用 Redis 分别定位当前 Gate 连接和有状态 Node。Whot 启用 Stateful 粘性路由，Ludo 保持普通无状态 `round_robin`。

Whot 的 `enterCommand` 由负载均衡选择实例，handler 从 context 取得 `node.Session` 并调用 `BindNode`；后续 `echoCommand` 和换 Gateway 重连都会回到该实例，`leaveCommand` 调用 `UnbindNode`。Handler 使用标准 `ctx + request -> response + error` 签名，不接触 Locator、Registry、NodeID 或 endpoint。

四个示例直接使用标准 `log/slog`，并把 service/app 属性传给 Kratos；日志输出策略由外层应用按部署环境配置，不由框架提供独立后端。

Gateway、Whot 和 Ludo 通过 `-id` 注入 Kratos instance ID。Whot 的 Enter 将玩家绑定到该 ID；同一逻辑实例重启必须复用原 ID，才能继续接收已绑定玩家。新增实例使用新的稳定 ID。Gateway ID 只要求在线实例间唯一，Ludo 是无状态服务。生产环境应由配置或 StatefulSet ordinal 分配稳定 Whot ID，不能使用包含 PID 的启动身份。

`env.Host` 默认使用 `127.0.0.1`，供内部 gRPC 监听和服务注册使用；跨主机运行时通过 `YOLA_ADVERTISE_HOST` 显式配置可达地址。示例 Registry 使用 etcd，地址由 `YOLA_ETCD_ADDR` 配置；Gateway 和 Whot 的 Redis Locator 使用 `YOLA_REDIS_ADDR`、`YOLA_REDIS_PASS`，在线事件使用 `YOLA_NATS_URL`（默认 `nats://127.0.0.1:4222`）。示例公告队列为 256、Payload 上限为 64KiB；生产应按实际事件大小和突发量重新配置。Redis 口令必须由运行环境注入，tracked source 不得保存非空默认值；生产安全要求见 [当前限制](../docs/issues.md)。这些配置仅用于本地测试。

运行服务端示例前必须先启动 Redis、etcd 和 Core NATS；示例 EventBus 使用在线 best-effort Pub/Sub，不启用 JetStream。

三个服务端示例共用同一 etcd Registry 工厂，实例注册使用 etcd lease。Whot 通过 `node.Locator(locateredis.New(redisClient))` 注入 Locator，并使用 `kratos.Metadata(nodeServer.Metadata())` 注册 sticky；Ludo 不配置 Locator，`Metadata()` 为 nil。Whot 通过独立 NATS Bus 发布公告，Gateway 的独立 Bus 订阅同一 Topic 并把 Payload 转成客户端 `AnnouncementCommand`；普通 `ChanSubscribe` 使每个在线 Gateway 都收到一份。Gateway 缓存 Registry 中的声明，不配置业务 service 名；只有 Whot 请求查询玩家 Node 绑定。Node 和 Gateway 都自行管理内部 gRPC，并通过 `kratos.BeforeStart(server.BeforeStart)` 在注册前完成依赖准备；Gateway 通过 `Transport` 拥有 TCP/WebSocket，应用只把 Gateway 列入 `kratos.Server`。

依次启动：

```powershell
go run ./examples/whot -id whot-1
go run ./examples/ludo -id ludo-1
go run ./examples/gateway -id gateway-1
```

启动第二个同名实例时必须同时指定不同 ID 和端口，例如 `go run ./examples/whot -id whot-2 -grpc-port 9003`。重启原实例时继续使用原 ID；复用 ID 只恢复路由可达性，游戏状态仍需业务自行恢复。

每个 Client 使用独立 UID；省略 `-uid` 时默认生成当前进程唯一的 `player-<pid>`。以下命令可直接验证两个 Whot Client 的 Gateway 广播，并同时观察 Ludo Client 接收跨 service 公告：

```powershell
go run ./examples/client -addr 127.0.0.1:3101 -service whot -uid player-1
go run ./examples/client -addr 127.0.0.1:3101 -service whot -uid player-2
go run ./examples/client -addr 127.0.0.1:3101 -service ludo -uid player-3
```

Client 启动后立即请求一次，此后每 3 秒请求一次；Echo Payload 携带发送方 UID。Whot 依次执行 Enter、Echo、Leave，输出 `<instance-id> say hello`，同时演示面向当前玩家的直接 Push 和经 NATS 广播到全部 Gateway 在线玩家的公告。一个 Whot Client Echo 后，全部在线 Client 都会输出包含其 UID 的 `announcement`，发起者还会收到自己的直接 Push。Ludo 只执行 Echo，但其在线 Client 也能收到 Whot 发布的公告。连接或请求失败后会在下个周期重新连接并认证，可用于观察 Gateway、Node、NATS 下线和恢复，按 Ctrl+C 退出。示例 token 仅用于本地链路测试，不是生产认证方案。
