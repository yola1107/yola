# 架构设计

本文只描述 Yola 的当前实现。接入方式见 [README](./README.md)，未关闭问题见 [当前限制](./issues.md)，性能数据见 [性能基线](./performance.md)。

## 1. 边界与组件

Yola 提供两类 `kratos.transport.Server`：

- `gateway.Server` 持有客户端 TCP/WebSocket 连接，负责认证、Gate lease、Node 发现、路由和回程 Push/Kick。
- `node.Server` 承载业务 command handler，负责 protobuf 适配、request-scoped `Session`、Node binding 和 epoch fencing。

每个 Kratos App 只有一组 Registry identity。Gateway App 与 Node App 必须独立装配；外层 App 自己拥有配置、日志、Registry 和 Redis client，Yola Server 只拥有自身运行状态及子 transport。

Gateway/Node 的停止语义和状态所有者各自独立，复用 listener、回程客户端等有实际共同职责的资源，不增加公共 BaseServer 或只负责转交的管理层。业务状态与执行模型留在 usecase、manager 或 mailbox。

```mermaid
flowchart LR
    C[Client] -->|TCP / WebSocket| G[gateway.Server]
    G -->|Node/Forward| N[node.Server]
    N -->|Gateway/Push| G
    G --> R[(Redis Locator)]
    N --> R
    G --> E[(etcd Registry)]
    N --> E
    N --> H[业务 handler]
```

| 包 | 职责 |
| --- | --- |
| `api/protocol/v1` | Client↔Gateway envelope、op 码和 `MaxProtoSize` |
| `api/cluster/v1` | Gateway↔Node 内部 gRPC 契约 |
| `network` | `Connection`、`ConnectionHandler`、`Invoker` 与 Transport 契约 |
| `network/tcp`、`network/websocket` | 独立的监听、编解码、心跳、发送队列和 Client |
| `gateway` | Session、认证、Gate lease、Discovery 和路由 |
| `node` | command 注册、Session 注入、Node binding 与 epoch 校验 |
| `locate`、`locate/redis` | Gate/Node 定位契约和 Redis 实现 |
| `instance` | Registry `sticky` metadata 契约 |
| `registry/etcd` | 共用的 etcd client 与注册租约所有者；条件创建、按本代 lease 注销，复用官方 Discovery/Watch 和 Session |
| `internal/clusterroute` | `GateBinding` 与集群 wire route 的边界转换 |
| `internal/grpcendpoint`、`internal/listener` | 内部 gRPC advertised endpoint 校验和 listener 所有权 |
| `internal/gateclient` | 直连目标 Gateway 的 gRPC ClientConn 池；调用方持有每次 RPC deadline |
| `internal/queue`、`network/internal/heartbeat` | 跨 transport 稳定复用的有界 callback 执行和单连接 heartbeat 原子状态；不持有 socket 或 transport 生命周期 |
| `network/internal/inbound` | 认证后的有界业务 FIFO 与独立心跳调度；读循环创建并停止，退出前等待在途 handler |
| `network/internal/auth` | 认证回复与认证前 Push 的公共规则；transport 保留帧读取、deadline 和错误身份 |
| `event`、`event/nats` | 在线、可丢失的 Publish/Subscribe 契约与 Core NATS adapter |

应用组装层创建并关闭进程专用的 `event.Bus`。Node usecase 只依赖 `event.Publisher`，Gateway 订阅 handler 固定 Topic 到客户端 command 的映射并调用 `gate.Broadcast`；Gateway/Node 不持有 Bus。事件在线可丢失，不重试、不重放、不补发离线消息，完整边界见 [EventBus 接入](./eventbus.md)。

独立的 `test` module 通过 `replace yola => ..` 使用框架；service 依赖 usecase，usecase 组装 player/table/robot，data 和 Node adapter 实现业务定义的存储、投递接口。usecase 拥有玩家和业务任务的生命周期，Table 的 mailbox 串行化桌内操作，框架不承担这些业务状态。

`test/internal/mailbox` 的 `TryPost` 只负责有界准入，已接纳的普通任务不随提交 context 取消；`Post` 的 context 只限制等待容量。`Call`/`PostAndWait` 由 `mailboxCall` 仲裁取消与开始：未开始可取消，已开始须等待结果，避免调用方提前回滚仍在执行的业务操作。队列、调度权与运行统计由同一把锁保护，在交接 worker 前完成状态更新。

## 2. 网络与存储

### 2.1 连接方向

| 连接 | 协议与解析 | 负载均衡 |
| --- | --- | --- |
| Client → Gateway | TCP：4B little-endian 长度 + `Proto`；WebSocket：Binary Message | 外部 LB/DNS |
| Gateway → Node | `discovery:///<service>` unary gRPC | `yola_wrr`；粘性请求按 Registry instance ID 精确选 SubConn |
| Node → Gateway | `direct:///<gate_endpoint>` unary gRPC | `pick_first` |
| Gateway → Gateway | 使用旧 `GateBinding` 直连并 Kick 重复登录连接 | `pick_first` |

Node 回程不经过服务发现。`GateRoute.gate_endpoint` 是认证时写入 Redis 的 Gateway 内部 gRPC 地址；Gateway 的 Registry 注册主要用于生命周期和运维可见性。

Gateway backend 按 service 复用发现连接，回程 gateclient 按 Gate endpoint 管理在途引用和空闲淘汰，二者不合并为通用连接池。Registry 实例集合与 picker 的 Ready SubConn 集合表示不同事实，不能互相替代。

每个 service 只创建一个 WRR ClientConn。backend pool 与 Gateway 同生命周期，service 名称来自有限的部署目录，因此不做按时间淘汰；首次连接由 pool 自有 context 和 `ConnectTimeout` 约束，单个请求取消只结束自身等待，不会取消其他等待者共享的连接创建，Gateway Stop 会取消全部在途连接。Registry 更新负责增删 SubConn；实例集合为空时必须清空旧 SubConn，使请求立即 fail closed。`gateway/balancer.go` 自建 balancer，`gateway/resolver.go` 自建 resolver，是因为 Kratos v3 内置 selector 的初始化顺序不能稳定取得 WRR builder，且内置 discovery resolver 会在空实例集合时沿用旧地址。Gateway 在请求入口创建 `RPCTimeout`，Node ClientConn 直接使用该 context，不再叠加第二个 client timeout。

TCP/WebSocket 在接纳连接与 Stop 之间使用同一 lifecycle owner：Stop 先禁止新连接，再关闭已提交连接并等待 handler/writer 退出；已 Accept 或完成 Upgrade、但尚未提交的连接必须在 Stop 后拒绝。listener 的临时 bind 失败不写入永久状态，独立 transport 可以在端口释放后重新执行 `BeforeStart`。

Gateway 实现可选的 `network.HeartbeatHandler`：认证仍同步执行，成功认证回复入发送队列后，业务消息进入单 worker 的 FIFO，心跳由读循环独立处理。Gateway 的业务锁与心跳锁分别串行本类操作，关闭按业务锁、心跳锁、binding 锁的顺序等待并撤下 Session；续租与 Forward 可以并发。middleware 必须允许同一连接的心跳与业务调用并发。未实现该能力的自定义 ConnectionHandler 保持原来的同步串行路径。

支持独立心跳的连接默认最多等待 8 个业务帧，可由 TCP/WS `RequestQueueSize` 调整，不计当前执行的请求。等待队列满、回复发送队列满或 handler 失败时关闭连接；停止先取消在途请求、丢弃尚未执行的业务帧，等待 worker 后再调用 handler.Close。认证前的 heartbeat/非法请求仍由 Gateway 拒绝。该模式下 TCP 回复使用非阻塞发送，避免等待发送容量阻塞心跳读取；慢 socket 仍受写超时及客户端存活判断约束。队列等待不计入单次 handler timeout，预算跟踪见 I46；新增连接内存见 [I50 调度成本](./performance.md#i50-dispatch-cost)。

TCP/WebSocket Client 的 connect、push、Kick 和 disconnect callback 由单一有界队列串行执行。认证期间收到的每个 Push 与 connect callback 都计入容量，并在 worker 启动前作为一个 batch 原子提交；容量不足时连接创建确定失败，不暴露部分 callback。关闭时拒绝新 callback、丢弃尚未开始的普通 callback，让当前 callback 完成后再依次执行 Kick 和 disconnect；连接资源先关闭，再放行 terminal callback，因此 callback 内调用 `Client.Close` 不等待自身。TCP/WebSocket 各自拥有 ticker、I/O 和关闭，只有 `idle → queued → writing → outstanding` 的原子状态迁移由无 transport 依赖的 heartbeat owner 复用。

`Connection.SendProto` 接纳不可变输入：调用期间和成功返回后，不得修改 Proto、Body 或其他可变字段的任何别名；可并发发送同一个只读消息。返回成功不保证已经编码或写入 socket，调用方若要更新内容，应先创建独立消息和数据（例如 proto.Clone），不能依据 WebSocket 当前同步编码的实现假定通用接口允许立即复用。TCP 仍可能保留输入指针，违规修改仍可能改变发送内容或产生 race。

网络 handler 与 middleware 必须在交还发送流程前完成对回复的修改，不得让后台任务继续写入已发送消息。自定义 codec 的 Marshal 须并发安全、在返回前完成对输入的只读访问，返回的编码 bytes 不能复用为可写 scratch。PreparedProto.Reset 只复用同步 fanout 视图，不恢复旧消息写入权；旧 Proto 仍可能由 fallback 连接持有。Gateway.Broadcast 会独立复制调用方 Payload，后续传给连接的消息保持只读。

认证回复的 Push 暂存、容量、op 与拒绝码规则由 `network/internal/auth` 统一处理。TCP/WebSocket 提供每次读取并解码一帧的函数，继续负责各自的帧错误、认证 I/O 取消及 deadline 清理；公共规则不改变 `ErrAuthenticationRejected`、Push 顺序或没有 Push 时的 nil 结果。

TCP/WebSocket 显式 endpoint 是各自 Server 持有的配置副本，`Endpoint()` 返回副本，scheme 必须与是否启用 TLS 一致：TCP 使用 `tcp`/`tcps`，WebSocket 使用 `ws`/`wss`，WebSocket path 必须与 `Path` 完全一致。自动 endpoint 优先使用 `AdvertiseHost`，其次使用非通配 listen host，最后选择 global-unicast interface；不能推导出可发布 host 时直接失败，不发布空地址。

WebSocket Server 私有持有 `http.Server`，调用方只通过 `TLSConfig`、`HandshakeTimeout`、`MaxHeaderBytes` 等显式 Option 配置，不得绕过 Yola transport 生命周期直接调用 `Serve`、`Shutdown` 或 `Close`。

WebSocket 默认 codec 由包内 protobuf 实现持有，不受 Kratos 全局同名注册影响；显式 `Codec`/`WithCodec` 使用自定义编码。`PreparedConnection.SendPrepared` 必须在返回前结束对 prepared 对象的访问，只能保留 `Marshal` 返回的不可变 bytes。

<a id="send-stats"></a>
TCP/WS Connection 提供可选的 `network.SendStatsProvider.SendStats()`，从现有发送队列读取 depth/capacity，累计 `QueueDropped` 仅记录 `ErrSendQueueFull`。`Closed` 表示关闭新发送准入，writer 可能仍在发送最后一帧；关闭、编码和 socket 写出错误不计入 QueueDropped。

`PendingPayloadBytes` 按每次发送的 `len(Proto.Body)` 累加，包含等待入队的回复/最后一帧和已排队帧，writer 取走或入队失败时减去。它不含 writer 当前帧、编码结果、channel/socket 开销，也不区分共享与独立 Payload，不能换算 RSS。关闭后尚未取走的帧仍计入该连接快照；释放连接才释放其队列。累计拒绝不因关闭清零，采样字段不保证同一时刻；观测不增加连接注册表、后台协程或消息队列，不改变发送背压。

### 2.2 Redis 模型

| Key | 值 | 生命周期 | 用途 |
| --- | --- | --- | --- |
| `locate:gate:{base64url(service\x00uid)}` | `GateBinding` JSON | 默认 60s；heartbeat 按需续租 | 定位物理连接并 fencing |
| `locate:node:{base64url(service\x00uid)}` | NodeID | 固定 6h；业务绑定时刷新 | 定位持有玩家状态的实例 |
| `locate:node:epoch:{base64url(service\x00nodeID)}` | 进程 epoch UUID | 30s；Node 每 10s 续租 | 阻止同 service、同 NodeID 双活 |

`GateBinding` 的 `ServiceName`、`UID`、`GateID`、`GateEndpoint`、`ConnID`、`BindingToken` 全部参与 fencing。Redis key 的 hash tag 固定使用 Raw URL Base64，输入由 `\x00` 分隔，不提供第二套 keyspace。

## 3. 生命周期

### 3.1 应用装配

Kratos App 按 `buildInstance` → 顺序执行 `BeforeStart` hooks → 调度 `Server.Start` → `Registrar.Register` → `AfterStart` hooks 运行；v3.0.0 不等待 Start 内部就绪。Node 通过 `kratos.Registrar(server.Registrar(registry))` 等待首次租约核验结果，失败时保留原始错误并拒绝登记。`buildInstance` 调用 `Endpoint` 时会触发内部 gRPC listener 的惰性 bind，Gateway/Node 在自身初始化回滚和 `Stop` 中关闭该 listener。

服务入口直接使用 `kratos.New`，显式配置 Context、StopTimeout、BeforeStart、Server 和按需 metadata。正常停止仍由 Kratos 调度。当前 Kratos v3.0.0 在 Endpointer、BeforeStart、注册或 AfterStart 失败时可能直接返回，应用所有者须在 `Run` 返回后取消自有 context，再用独立的 10s 预算调用 Server.Stop，最后关闭 EventBus、Registry、Redis 等外部依赖。`Run` 的原始错误保持不变，额外清理错误单独记录。

启动失败仍由应用所有者调用 `Server.Stop`，不以 `App.Stop` 代替本地资源回收。共用的 `registry/etcd` 只撤销自己申请的 lease，不按 key 盲删；采用其他 Registrar 时，应用须自行核对其所有权语义。Registry 的 etcd Session 绑定到 client 生命周期，关闭 Registry 后停止续租；未显式注销的记录按 15s TTL 回收。

`registry/etcd` 从原 `test/internal/registry/etcd` 提升并统一使用；examples 只提供环境变量装配，`test` 不保留第二个 Registry 实现。注册使用 etcd `CreateRevision == 0` 事务，保留原 namespace、ServiceInstance JSON 和实例 ID。已有同 service/ID 时返回 `ErrInstanceExists`，调用方等待旧记录回收后重建应用；每个 Registry 只尝试注册一次。失败回收使用独立 3s 预算，保留回收失败凭据供 Deregister 重试。

续租复用 etcd `concurrency.Session`，只续当前 lease，丢失后不自动重新 Grant/Put，应用需要重启以恢复登记；不隐式增加自动重启策略。此约束避免旧进程重写新记录。就绪适配和条件登记仍不是跨 Redis/etcd 事务，不撤销已经发出的业务写入；I48 的存储侧代次保护单独处理。

Registry、Redis 和 EventBus 由创建它们的应用层关闭；EventBus 的订阅和释放语义见 [EventBus 接入](./eventbus.md)。Ludo/Whot 的 App provider 返回 Wire cleanup，生成的关闭顺序为 Node → Registry → usecase → Redis；usecase 的重复 Drain 沿用自身幂等规则。

Gateway/Node 实例不支持生命周期重试，也不支持外部并发调用 `BeforeStart` 与 `Stop`。初始化 owner 服从 hook context：并发重复初始化只拒绝后来者；owner 失败同步回滚并进入终态，成功后再次初始化也进入终态。误用时的保护为：Stop 先关闭准入并等待准备结束，初始化提交再次检查终态，不能发布新 identity；Stop 等待超时后，初始化 owner 仍负责完成回滚。

正常停止共用 `kratos.StopTimeout` 提供的预算，业务 Drain 服从同一 context。`Run` 返回后的重复 Stop 不会重做业务 Drain，也不会绕过失败排空释放 epoch；只有先前已允许释放、但注销未完成的 epoch 可以重试。Table 和 mailbox 提供 context-aware 关闭入口。

### 3.2 Gateway

`NewServer` 校验依赖和 Option，并为 TCP/WebSocket 安装 handler。`BeforeStart` 校验 App identity、准备内部 gRPC endpoint、执行 Locator Ping，再准备客户端 transport；失败时按逆序回滚，每个资源使用独立的 `RPCTimeout`。`Start` 必须在准备完成后调用，开放客户端准入并启动客户端 transport 和内部 gRPC。

`Stop` 按顺序执行：

1. 关闭客户端准入，等待已接纳认证及其同步 takeover Kick。
2. 停止 broadcaster，从本地 Session Registry 一次性交接全部 Session。
3. 有界 worker 通过共享索引领取 Session，在停止预算内发送 Kick、关闭连接并清理定位。
4. 停止 TCP/WebSocket、内部 gRPC 和 backend ClientConn，汇总错误。

### 3.3 Node

command 与 disconnect handler 必须在 `BeforeStart` 前注册，运行期不变。`BeforeStart` 校验 App identity、准备 gRPC endpoint、校验 sticky/Locator 一致性、执行 Ping 并按需申请 epoch。已确认注册成功后发生取消或提交失败，由准备 owner 持本次凭据回滚。

Stateful Node 的 `Start` 先检查本地有效期并完成首次续租核验，再开放 gRPC；任一核验失败都拒绝启动。自身启动失败以独立的 `PushTimeout` 预算调用完整 Stop，保留业务 Drain 和 epoch 释放条件。[node/lifecycle.go](../node/lifecycle.go)

两个 `requestAdmission` 分别拥有入站请求和出站副作用（Bind/Unbind/Push）的终态、在途计数及排空信号。已通过就绪检查的 Registry 登记也计入 requests，保证正常 Stop 在登记 I/O 返回前不释放 epoch；停止等待本身会关闭对应准入。`Stop` 按顺序执行：

1. 拒绝新的 Forward/Disconnect，等待已接收请求返回。
2. 执行业务 Drain；期间 Session 的 Bind/Unbind/Push 及 `PushToUID` 仍可使用，Drain 返回前必须停止自身的绑定与推送生产者。
3. 关闭出站副作用准入，等待已接纳的绑定写入与 Push 完成；保存 Session 后发起的后台操作也纳入此屏障。
4. 停止 gRPC，按排空结果处理 epoch，最后关闭 Gateway ClientConn。

只有请求、业务 Drain 和出站副作用均排空，才撤下可服务 identity、停止并等待续租任务、按固定代次注销 epoch。任一排空失败或超时则停止续租，保留 epoch 到 TTL 回收，重复 Stop 不会绕过失败排空提前释放。已经获准释放、但等待续租任务退出超时或注销失败的凭据，可由后续 Stop 重试；NotFound/Conflict 视为本代已不再持有 key。

Node 不持有 EventBus、Table、玩家或业务后台任务。Session 使用时读取当前身份，不缓存永久有效的 identity；正常停机后保存的 Session 也不能继续绑定或推送。Registry 摘流传播期间，旧路由可能收到 `Unavailable`，Gateway 不做补偿重试。

### 3.4 Node epoch

`epochLease` 拥有本代固定凭据、本地有效期、续租任务和释放状态，只依赖申请、续租、注销三项存储能力。Server 的生命周期锁负责准备和身份交接；identity 与 lease 指针供请求原子读取，lease 自己串行化注销 I/O。[node/epoch.go](../node/epoch.go)

- TTL 为 30s，每 10s 续租；申请和单次续租的 context 最长 3s，续租还受原租约剩余时间限制。
- 本地截止时间使用单调时钟，从成功申请或续租的调用开始计算。迟到响应不能延长或恢复已经失效的身份。
- 运行期普通续租错误只在原有效期内重试；NotFound/Conflict 或本地过期会关闭两类准入，使 Start 返回 lifecycle fatal。
- 过期监视与续租 I/O 独立，存储忽略取消也不会推迟本地失效。Stop 等待任务退出，超时报告错误并保留凭据；框架不能强制终止不响应 context 的实现。
- Forward（包括空 sticky claim）、Disconnect、Session 绑定和 Push 都校验有效期，Node binding 查询返回后再次检查。已接纳操作的 context 随租约失效取消，业务 Drain 使用 Stop 预算。

本地有效期不撤销已执行的业务写入，也不替代业务事务 fencing；忽略 context 的工作可能持续到进程退出。fatal 路径不承诺 Kratos 显式调用 `Registrar.Deregister`，摘流仍取决于进程退出和注册实现。

## 4. 请求链路

### 4.1 认证与 Gate lease

```text
Client OpAuth
  -> Gateway Authenticator：校验 service/token，返回规范化 UID
  -> Redis BindGate：写入新 binding，原子返回旧 binding
  -> Gateway 提交本地 Session
  -> 若旧 binding 不同，同步 best-effort 直连旧 Gateway 发送 Kick
  -> 回复 OpAuthReply
```

`AuthTimeout` 从连接 `Open` 起算，Authenticator、`BindGate` 和本地提交共享同一 deadline。认证不要求目标 Node 在线。正常断开的 Unbind 与 Disconnect 通知共享一个独立 `RPCTimeout`，不会继承已取消的连接 context，也不会为每一步重新计时；异常退出依赖 TTL。heartbeat 仅在本地剩余 lease 不超过 `LeaseTTL/2` 时访问 Redis 续租。

### 4.2 业务请求

```text
Client OpRequest
  -> Gateway 校验 Session 与 Gate lease
  -> 读取 service 的 sticky 声明
  -> stateless：WRR 选择 Node
  -> sticky 未绑定：查询 Node binding 后使用 WRR
  -> sticky 已绑定：查询 Node binding + epoch，按 NodeID 精确选 SubConn
  -> Node 校验 service 与本地租约；有 sticky claim 时校验 NodeID/epoch 并再次查询 binding
  -> 注入 Session，解码 protobuf，调用 handler
  -> Gateway 将 gRPC status/body 写入 OpResponse
```

Node 对已绑定请求在 handler 前再次查询 binding，承担改绑 fencing；Redis 调用数量和不能合并的原因见 [性能基线](./performance.md#热路径成本)。Gateway session 的 handler 锁串行化 Handle/Close，binding 锁允许远程 Kick 在 handler 执行期间使路由失效，两者不能合并。

### 4.3 Push、Kick 与 Disconnect

- `Session.Push` 使用当前请求携带的 `GateRoute`，不会重新定位玩家的新连接。
- `Server.PushToUID` 先通过 Locator 查当前 Gate，再直连目标 Gateway。
- Node 回程保留调用方 context 和既有 gRPC status；Gateway 不可用映射为 `Unavailable`，非法 endpoint、TLS 不匹配或 Locator 返回不一致 route 映射为不泄漏底层地址的 `Internal`。
- Gateway Push/Kick 会校验目标 Gate identity、完整 binding、lease 和 `MaxProtoSize`，发送队列满时返回 `ResourceExhausted`。
- 客户端断开和 Gateway 正常停机时，Gateway best-effort 调用 `Node/Disconnect`；重复登录触发的连接替换 Kick 不发送该通知。`Disconnect` 是 at-most-once 的连接事件，可能晚于最后一次 Forward 或玩家重连，不等同业务 Logout。

`node.ClientMiddleware` 将 Kratos client middleware 按配置顺序安装到 Node → Gateway RPC，覆盖 `Session.Push` 和 `PushToUID` 的 gRPC 阶段；`node.Middleware` 仍只处理入站 command。Yola 沿用调用方的 Push deadline，不因接入 middleware 增设超时、重试或后台任务。

`node.CommandFromContext(ctx)` 返回实际 Forward 的 `(command int32, present bool)`，typed middleware、typed handler 和 RawHandler 均可读取。同一请求类型用于多个 command 时以此 ID 区分；0 是可存在的合法注册值，未注入元数据的 context（含 nil）返回 false。dispatch 在原有 handler 查找后写入不可变的 context 值，不增加第二套路由表，也不改变 `/cluster.v1.Node/Forward` 的 Kratos operation、Session 或 middleware 顺序；RawHandler 仍不自动应用 node.Middleware。

应用可注入 Kratos OTel metrics/tracing middleware，并自行创建和关闭 exporter/provider；Yola 不设置全局 OTel SDK。`LocateGate`、桌 fanout 和 mailbox 等待不在 RPC middleware 的计时范围内。

Gateway 为 Forward 查询 epoch，Disconnect 仅共用 Node 路由定位，不额外查询 epoch；二者不能合并为失败语义相同的转发流程。

业务处理 Disconnect 时必须在实际状态所有者（例如 actor/mailbox）内比较 `Session.BindingToken()` 与玩家当前 Session；旧 token 的通知不得修改新连接状态。支付、结算和状态写入仍需业务提供幂等、事务条件或串行化。

Whot Player 统一持有 Session 和离线标记：连接替换、恢复在线与按 token 标记离线使用同一把锁，桌内状态仍由 mailbox 串行化。换桌期间也可记录当前连接断线；排队的旧 Disconnect 到达桌任务后只忽略事件，不回写新连接的离线状态。

## 5. Stateful 粘性路由

`Stateful` 表示业务实例持有玩家状态；`sticky` 是 Yola 的 service 级路由能力。Node 通过 Registry metadata 声明 `sticky=true`，Gateway 不维护业务 service 名单。

当前配置 Node Locator 同时启用 Stateful 租约与绑定能力，Stateless Node 因此不能仅为了 `PushToUID` 配置 Locator。只有出现明确的这类调用需求时，才拆分 Gate 寻址与 Node 租约能力。

- 未声明 `sticky`：使用 service 级 WRR，不访问 Node Locator。
- `sticky=true` 且未绑定：查询结果为空后使用 WRR，handler 可调用 `Session.BindNode`。
- `sticky=true` 且已绑定：精确投递到 NodeID；目标不可用、Locator 失败或 binding 变化时 fail closed，不改投其他 Node。
- `Session.UnbindNode` 仅在当前 NodeID 匹配时删除；`BindNode` 覆盖 NodeID 并刷新 6h TTL。
- Gateway 首次使用 service 时固定其 `sticky` 模式；endpoint 和 NodeID 继续随 discovery 更新，模式变化则 fail closed。`sticky` 是路由语义而非实例健康属性，滚动发布期间可能出现新旧声明混合，因此 Stateful/Stateless 切换必须重启全部 Gateway。

Gateway 不缓存玩家 Node binding 或未绑定结果。Gate close、takeover 和 heartbeat 不修改 Node binding，因此玩家换 Gateway 重连后仍能定位原 Node。Yola 只保证实例定位，不保证内存状态恢复、同 UID 串行、actor 唯一性、幂等或故障迁移。

### 5.1 故障与竞态

| 场景 | 当前行为 |
| --- | --- |
| Gateway 宕机 | Gate binding 随 TTL 清理；Node binding 保留到自身 TTL |
| 玩家换 Gateway 重连 | 新认证覆盖 Gate binding；后续请求仍定位原 Node |
| Node 宕机或网络黑洞 | 等待 gRPC 连接退避/超时或返回 `Unavailable`，不随机改绑 |
| Node 使用原 ID 重启 | Registry/gRPC 恢复后原 binding 可继续使用 |
| Node 使用新 ID 重启 | 旧 binding 在 TTL 内不可达，业务显式改绑或等待过期 |
| 未绑定玩家并发首请求 | 可能进入不同 Node，最终 binding 为 last-write-wins |
| 改绑与旧请求并发 | Node 二次定位阻止未进入 handler 的旧请求；已开始的操作不会撤销 |
| 同 service、同 NodeID 双活 | 后启动者注册 epoch 失败；epoch mismatch 返回 `Aborted` |
| Redis 不可用 | 依赖该次查询的认证和粘性请求失败；Node 续租错误在本地有效期内重试，到期关闭准入 |
| prepared Node 租约过期或被替代 | Start 核验失败，不开放 gRPC |

6h Node binding TTL 是失效缓存上限，不是存活探测。持续时间更长的业务必须在成功请求中幂等刷新绑定。

## 6. 协议、错误与默认值

### 6.1 外部协议

| Op | 方向 | 说明 |
| --- | --- | --- |
| `OpAuth` / `OpAuthReply` | C→S / S→C | 每连接一次，认证 service 与 token |
| `OpHeartbeat` / `OpHeartbeatReply` | C→S / S→C | 按需续租；TCP 顺延 read deadline，WebSocket 使用 channel read deadline |
| `OpRequest` / `OpResponse` | C→S / S→C | 按 `seq` 关联，`cmd` 路由到 handler |
| `OpPush` | S→C | Node 主动推送，无 `seq` |
| `OpKick` | S→C | 最后一帧后关闭，`code` 说明原因 |

序列化后的 `Proto` 不得超过 `MaxProtoSize = 4096`。`Proto.code` 是唯一框架状态；路由、fencing、transport 和 handler error 经 gRPC status 映射，业务状态放在具体响应 body 中。

### 6.2 默认值

| 层 | 配置 | 默认值 |
| --- | --- | --- |
| Gateway | gRPC handler / `RPCTimeout` / `AuthTimeout` / `LeaseTTL` | 3s / 3s / 15s / 60s |
| Gateway 非请求预算 | `ConnectTimeout` / `LeaseTimeout` / `CleanupTimeout` | 3s / 3s / 3s |
| Gateway broadcast | worker / queue | `min(8, GOMAXPROCS)` / 256 |
| Node | gRPC handler / `PushTimeout` | 3s / 3s |
| Node 启动失败回滚 | `CleanupTimeout` | 3s |
| TCP Server | handler / handshake / heartbeat / write / send queue | 3s / 15s / 15s / 10s / 32 |
| TCP Client | ping / read / write / send queue | 5s / 15s / 10s / 100 |
| WebSocket Server | handler / handshake / read / write / send queue | 3s / 15s / 60s / 10s / 32 |
| WebSocket Client | ping | 15s |
| TCP/WebSocket Server（HeartbeatHandler） | 等待业务帧数 | 8，不含当前执行请求 |
| 连接限制 | `MaxConnLimit` / `MaxConnPerIP` | 10,000 / 100 |
| Redis Locator | Node binding TTL | 6h |

`network.DefaultHandlerTimeout` 统一为 3s，供 TCP/WebSocket 和 Gateway/Node 内部 gRPC 限制单次入站 handler；调用方更短的 deadline 仍优先。TCP/WebSocket 可用 `Timeout`、Node 可用 `HandlerTimeout` 显式覆盖。`AuthTimeout` 只限制未认证连接的总生命周期，不能替代单次 handler deadline。TCP Client read timeout 必须大于 ping interval。TCP/WebSocket Client 都是单连接生命周期，断开后由调用方创建新 Client。连接总量与 per-IP 上限相互独立，压测和经代理部署必须分别核对。

### 6.3 请求预算与超时职责

当前超时分散来自两类职责：同一请求在各入口受到独立上限保护，入桌、推送和失败清理又各有生命周期。沿同一个 context 派生的 deadline 取最早截止时间；后续层重新设置更长的 `WithTimeout` 不会延长父 context。因此只放宽 Gateway RPC，仍可能被 WebSocket 或 Node 默认的 3s 截断。

| 位置与所有者 | 当前预算 | 覆盖范围与传递边界 |
| --- | --- | --- |
| WebSocket Client `Request` | 默认 30s | 限制客户端等待响应；外部 `Proto` 没有 deadline 字段，客户端请求截止时间不随消息传入服务端 |
| TCP/WebSocket `NewInvoker` | 默认 3s；测试 Gateway 由 `-rpc-timeout` 覆盖 | 为单条入站消息创建 handler context，Gateway Forward 继承它 |
| Gateway `Forward` | `RPCTimeout` 默认 3s | 从入站 context 派生，覆盖路由定位和 Node RPC；内部 gRPC Client 已关闭 Kratos 隐式 2s 上限 |
| Gateway 依赖准备与 backend 创建 | `ConnectTimeout` 默认 3s | BeforeStart 的 Locator Ping 服从更短父 deadline；共享 backend 创建使用 pool 自有 context，调用方只控制自身等待 |
| Gateway Gate lease 续租 | `LeaseTimeout` 默认 3s | 单次续租 I/O 上限，仍服从 heartbeat handler 更短的 deadline；不随 RPCTimeout 改变 |
| Gateway 断线/Kick/回滚/Session 排空 | `CleanupTimeout` 默认 3s | 断线和 Kick 清理独立于调用方取消；单 Session 排空服从 Stop 的总 deadline，每项启动回滚有独立预算 |
| Node gRPC handler | 框架默认 3s；Ludo YAML 为 5s；Ludo 压测夹具为 15s | gRPC 传播上游 deadline，Node `HandlerTimeout` 进一步限制处理时间 |
| Ludo 首次入座、重连 | `playerEnterTimeout = 5s` | BindNode 后从独立 background context 创建；截止时只能取消尚未开始的 mailbox 任务，已开始则等待结果 |
| Ludo 入座失败清理 | `playerCleanupTimeout = 2s` | 失败后创建新的独立 context 执行解绑，不能复用已过期的入座 context |
| Whot 首次入座、重连 | `playerEnterTimeout = 2s` | BindNode 后使用独立 context 等待桌任务；未开始可取消，已开始等待完成 |
| Whot 入座失败清理 | `playerCleanupTimeout = 2s` | 入座或重连失败后重新创建独立 context 执行解绑，保留入座与清理的错误链 |
| Node `PushToUID` / Session `Push` | `PushTimeout` 默认 3s | 覆盖单次 LocateGate 与回程 RPC；桌推送当前从 background context 发起，每次 Push 分别计时 |
| Node 准备与启动失败回滚 | `CleanupTimeout` 默认 3s | 独立于失败的 caller context 和 PushTimeout；正常 Stop 仍使用调用方 context |

代码入口为 [请求封套](../api/protocol/v1/protocol.proto)、[入站 handler](../network/invoke.go)、[Gateway Forward](../gateway/forward.go)、[Node 装配](../node/server.go)、[Ludo 入座](../test/ludo/internal/biz/handle.go)、[mailbox 等待](../test/internal/mailbox/group.go) 和 [Node Push](../node/push.go)。测试 Gateway 已把同一参数传给 WebSocket handler 和 Gateway RPC；游戏 Node 仍由各自配置装配。调整 RPCTimeout/PushTimeout 不再连带修改非请求预算；部署确有不同依赖或清理窗口时，分别配置对应 Option。

Ludo 入座预算不包含创建玩家和 BindNode，且 `Seat` 中多次同步 Push 可以使已开始的任务超过 5s。外层 deadline 到期也不能撤销已开始的桌内操作；Node handler 与入座等待都为 5s 时，不能保证排队后还有足够时间执行、清理和返回。单 Gateway 四轮成功数据使用的 WebSocket/Gateway/Node 预算均为 15s，不能据此证明 YAML 的 5s Node 配置具有相同突发容量，详见 [参数对照](./performance.md#ludo-单-gateway-参数对照)。

当前采用以下预算策略；完整游戏和同配置负载仍按 I46/I45 验收：

1. 应用装配分别表达 transport、Forward 与 Node 的上限；测试 Gateway 的同一请求参数只配置 WebSocket handler 和 Gateway RPC。跨进程按部署策略对齐，框架不依赖游戏入座常量，不新增覆盖全部职责的全局 timeout。
2. 保留每层上限；有效 deadline 是父 context 与本层上限的较早值，不改为“只有无 deadline 才补默认值”。Node 独立调用仍受 HandlerTimeout 保护。客户端等待 deadline 不进入现有 Proto；I50 的业务 FIFO 等待时间也不计入从实际调用开始的 handler 上限。
3. mailbox 尚未开始的工作允许取消；已开始的业务按现有契约等待完成或回滚。Ludo/Whot 的独立入座与清理预算保持不变，不直接替换为已过期的请求 context。取消客户端等待不能撤销已经发送或已开始的操作。
4. 非请求预算按 owner 分离。Gateway 的依赖准备、续租、清理分别由 ConnectTimeout、LeaseTimeout、CleanupTimeout 限制；Node 启动失败回滚由 CleanupTimeout 限制，epoch 注册/续租仍有独立 3s I/O 上限。正常 Stop 的总预算由调用方持有，外部依赖仍由应用关闭。

根包已验证真实 gRPC 上 transport/Forward/Node/父 deadline 各自最短时的预算和响应 Code，以及独立清理、续租和共享建连；扩展包已验证排队取消、开始后完成、独立清理和重连 Session 归属。完整游戏及同配置负载未在本轮运行，因此不调整游戏默认预算、不宣称 I46/I45 的部署验收完成。跟踪项见 [I46](./issues.md#功能与语义缺口)。

## 7. 部署与安全约束

- Gateway 与 Node 必须使用独立 App 和 Registry identity；Stateful Node instance ID 必须稳定且在线唯一。
- Gateway、Node 与 Locator 必须来自同一版本；首版不支持旧 epoch/key、空 epoch 或新旧协议混部。
- 跨主机部署时，Registry endpoint 和 `GateBinding` 中的 Gateway endpoint 必须对调用方可达。
- 内部 gRPC TLS 的 client/server 配置必须成对启用，只接受与证书配置一致的 `grpc://` 或 `grpcs://` scheme；endpoint 必须是非零数字端口的纯 `host:port`，不得携带 userinfo、path、query 或 fragment。
- Server TLS 必须提供 `Certificates`、`GetCertificate` 或 `GetConfigForClient` 之一，不允许 `InsecureSkipVerify`。通过 TLS Option 接入时 clone `tls.Config` 的顶层配置；调用方负责共享证书元素、证书池及动态 callback 的并发安全。经代理暴露客户端真实 IP 前必须先定义可信代理边界。
- Redis/etcd 集成测试只能使用专用实例和隔离 prefix；生产环境必须使用 secret 管理、ACL/mTLS、入口限流和容量验收。

未闭环的部署和性能条件统一记录在 [当前限制](./issues.md)。
