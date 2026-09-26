# 架构契约

本文描述当前实现。运行与开发入口见 [README](./README.md)，未闭环事项见 [当前问题](./issues.md)。

## 1. 边界与组件

```mermaid
flowchart LR
    C[Client] -->|TCP / WebSocket| G[gateway.Server]
    G -->|Node.Forward| N[node.Server]
    N -->|Gateway.Push| G
    G --> R[(Redis Locator)]
    N --> R
    G --> E[(etcd Registry)]
    N --> E
    N --> H[业务 handler]
```

Gateway 与 Node 是原生 Kratos App 的 `transport.Server` / `transport.Endpointer` 组件，内部持有 Kratos gRPC Server。每个 App 只有一组 Registry identity，Gateway App 与 Node App 必须独立装配。

| 所有者 / 包 | 职责 |
| --- | --- |
| 外层 Kratos App | identity、配置、日志、完整 transport 列表、hooks、注册及外部依赖回收 |
| `gateway` | 物理 Session、认证、Gate lease、发现、路由、回程 Push/Kick 和广播 |
| `node` | command 注册、protobuf/middleware 适配、request-scoped Session、epoch 与排空 |
| `network`、`network/tcp`、`network/websocket` | 公共连接契约及两种独立的监听、编解码、心跳、队列和 Client |
| `locate`、`locate/redis` | 定位接口、存储编码和原子条件修改 |
| `registry/etcd`、`instance` | 条件注册/本代租约回收、Discovery/Watch 与 sticky metadata |
| `internal/listener`、`internal/gateclient` | listener 所有权、按 Gateway endpoint 复用并回收回程连接 |
| `network/internal/*`、`internal/queue` | 公共认证、心跳、请求跟踪及有界执行规则，不接管 socket 生命周期 |
| `event`、`event/nats` | 在线 Pub/Sub；由应用创建、订阅并关闭，详见 [EventBus](./eventbus.md) |
| 业务 service/usecase | 玩家、Table、事务、幂等与并发执行模型；框架不持有这些状态 |

### 1.1 接入与服务边界

同进程 handler、普通 HTTP/gRPC 入口可直接调用同一 usecase；直接 Go 调用不会自动附加 Node middleware、Session 或预算。共享可变状态和跨入口 Drain 由业务 owner 统一管理：Kratos 并行停止各 transport，Node 只跟踪自身请求，Server 列表顺序不构成排空屏障。

`node.Register` 注册长连接 command，不会自动转换普通 gRPC service。跨进程业务依赖使用应用注入的 Kratos client；`api/cluster/v1` 只承担框架 Forward/Push/Kick/Disconnect。普通入口不得伪造 Session 或路由，需要按 UID 推送时使用已配置 Locator 的 `PushToUID`。

## 2. 网络与存储

### 2.1 连接方向

| 连接 | 协议 / 寻址 | 选择方式 |
| --- | --- | --- |
| Client → Gateway | TCP：4B little-endian 长度 + Proto；WS：Binary Message | 外部 LB/DNS |
| Gateway → Node | `discovery:///<service>` unary gRPC | `yola_wrr`；粘性请求精确选择 NodeID |
| Node → Gateway | `direct:///<gate_endpoint>` unary gRPC | `pick_first` |
| Gateway → Gateway | 按旧 GateBinding 的 endpoint 直连 Kick | `pick_first` |

回程不 discovery Gateway，地址来自认证时写入的 GateBinding。Node 的发现记录必须指向实现 `cluster.v1.Node` 的 endpoint；resolver 按安全配置选择第一个合法 grpc/grpcs endpoint，不识别业务 RPC 与内部 RPC 角色，HTTP endpoint 不参与选择。内部 gRPC Server 不作为外层任意 service 的注册容器。

Gateway 按 service 复用一个 WRR ClientConn，service 来自有限部署目录，不按时间淘汰。首次连接使用 pool 自有 context 和 ConnectTimeout，单个请求取消只结束自身等待；Gateway Stop 取消共享建连。Registry 空快照须清空旧地址并 fail closed；精确路由等待目标 SubConn Ready。发现实例集合与 Ready SubConn 集合不能互相替代。[backend](../gateway/backend.go)、[resolver](../gateway/resolver.go)、[balancer](../gateway/balancer.go)

回程 gateclient 按 endpoint 管理在途引用与空闲回收，和上述 service pool 有不同生命周期。TCP/WS 也各自保留 I/O、deadline、最后一帧和关闭逻辑；Stop 禁止接入后，已 Accept/Upgrade 但尚未提交的连接仍须拒绝。独立 transport 的临时 bind 失败可在端口释放后重试。

Gateway 实现可选的 `network.HeartbeatHandler`：认证同步执行，成功回复入发送队列后，业务进入单 worker FIFO，心跳由读循环独立处理。默认等待8个业务帧，不含当前请求；middleware 须允许同连接心跳与业务并发。队列满、回复发送队列满或 handler 失败会关闭连接；停止取消在途请求、丢弃等待帧，等待 worker 后调用 handler.Close。未实现该能力的 handler 继续同步串行执行，队列等待不计入 handler timeout。

Client 的 connect、push、Kick、disconnect callback 由有界队列串行执行。认证前 Push 与 connect callback 一起计入容量并原子入队，超限时创建失败；关闭丢弃未开始的普通 callback，当前 callback 完成后依次执行 Kick、disconnect。连接资源先关闭再放行终止 callback，callback 内 Close 不等待自身。

`Connection.SendProto` 接纳不可变消息：调用期间和成功返回后不得修改 Proto、Body 或其别名，可并发发送同一只读消息；更新时创建独立副本。成功仅表示接纳，不保证已经编码或写出。handler/middleware 须在交还回复前完成修改；自定义 Marshal 必须并发安全、只读访问输入，返回 bytes 不得复用为可写 scratch。

`PreparedProto` 使用后不得复制，Reset 只能在上一批 SendPrepared 全部返回后执行；它不恢复旧消息写入权。SendPrepared 返回前须结束对视图的访问，只能保留不可变编码 bytes；fallback 仍可能持有旧 Proto。Gateway.Broadcast 另行复制调用者 Payload。WS 默认 protobuf codec 不受 Kratos 全局同名注册影响，显式 custom codec 保留自身行为。

TCP/WS Server 持有显式 endpoint 的副本，Endpoint 返回副本。scheme 与 TLS 匹配（tcp/tcps、ws/wss），WS path 与 Path 一致；自动地址依次取 AdvertiseHost、非通配 listen host、global-unicast interface，不能推导时失败。WS 的 http.Server 为私有资源，通过 Option 配置，不绕过组件调用 Serve/Shutdown/Close。

<a id="send-stats"></a>
**发送观测。** Connection 可实现 `SendStatsProvider`：depth/capacity 来自原队列，QueueDropped 只累计 `ErrSendQueueFull`；Closed 表示拒绝新发送，writer 可能仍在发送最后一帧。PendingPayloadBytes 按每次发送的 Body 长度计等待入队及已排队数据，writer 取走或入队失败时扣除；不含当前帧、编码、socket 或对象开销，不能换算 RSS。关闭不清累计值，残留队列仍可观测；字段不是跨项原子快照。

### 2.2 Redis 模型

| Key | 值 | 生命周期 |
| --- | --- | --- |
| `locate:gate:{base64url(service\x00uid)}` | GateBinding JSON | 默认60s；heartbeat按需续租 |
| `locate:node:{base64url(service)}:base64url(uid)` | NodeID | 固定6h；Bind刷新 |
| `locate:node:epoch:{base64url(service)}:base64url(nodeID)` | epoch UUID | 30s；Node每10s续租 |

编码使用无填充 Raw URL Base64。GateBinding 的 ServiceName、UID、GateID、GateEndpoint、ConnID、BindingToken 全部参与条件比较。Gate key 按 UID 分布，Node binding/epoch 按 service 共用 slot；一个 service 的 Node 读写集中在一个 slot，是容量边界。

<a id="node-binding"></a>
**Node binding 修改权。** Session 从 Node 本代 lease 取得 epoch。单机/Cluster 共用 Lua，原子核验 epoch 后执行 Bind 覆盖或 Unbind；Unbind 仅删除相同 NodeID，binding 缺失或指向其他 Node 时幂等无操作。epoch 缺失/冲突会使 Node 关闭准入并取消本代工作；普通依赖错误或 caller 取消不自动撤销整个 Node。原 ID 重启取得新 epoch 后可继承原 NodeID binding，旧 epoch 的迟到写被拒绝。[key编码](../locate/redis/decode.go)、[Lua](../locate/redis/script.go)

进程修改权不等于玩家业务所有权：仍有效的 A 可在玩家改绑 B 后再次 Bind 覆盖 B，不能把 Bind 当作安全保活。物理连接归属又由 GateBinding/BindingToken 独立表示。本地 lease 失效和 Redis epoch 替换不在同一原子时刻；context 取消不能撤销已发送 I/O。Lua 只保证当前 Redis 执行节点的原子修改，不保证异步复制切主后已确认数据不回退。当前不支持旧 key 布局 fallback/双写，也不自动清理旧数据。[I03](./issues.md#i03)

## 3. 生命周期

### 3.1 应用装配

- Kratos v3.0.0 按 buildInstance（调用 Endpoint）→ BeforeStart hooks → 调度 Server.Start → Register → AfterStart 运行，不等待 Start 内部就绪。Node 必须用 `server.Registrar(registry)` 等待首次租约核验，失败保留原错误并拒绝登记；listener owner 回收 Endpoint 提前绑定的资源。
- Server、Metadata、Registrar Option 是覆盖语义，hooks 是追加语义。应用一次传入完整 transport 列表，合并业务 metadata 与 Node.Metadata；Stateful 由 Locator 配置决定。
- Run 可能在 Endpoint/hook/注册失败时直接返回。应用须取消自有 context，用独立有界预算（示例10s）调用 Server.Stop，再关闭 EventBus、Registry、Redis；保持原 Run 错误，清理错误另行记录。额外 transport/hooks 的资源仍由应用回收。
- 正常 Stop 使用 Kratos 传入的预算，业务 Drain 共用该预算；StopTimeout 不是整个 App 停止的总上限，Registrar 有独立超时。v3.0.0 显式 App.Stop 在注销失败时可提前返回，应用仍须主动取消与清理。
- Server 实例不支持生命周期重试或外部并发 BeforeStart/Stop；初始化失败同步回滚并进入终态。组件允许重复 Stop，但不会重复业务 Drain、绕过失败排空释放 epoch，或保证任意外部资源也可重复关闭。Stop 超时不表示后台工作已全部结束。

`registry/etcd.New` 由应用创建并 Close。每个 Registry 只尝试注册一次，以 etcd CreateRevision==0 创建空 key；同 service/ID 已存在时返回 ErrInstanceExists，默认等待旧15s lease失效后重建应用。它复用官方 Discovery/Watch 和 Session，只续本代 lease，失租不自动重新登记；Deregister 只撤本代 lease，失败回收使用独立3s预算并保留可重试凭据。Registry.Close 停止续租，未注销记录按TTL回收；这些操作不是跨 Redis/etcd 的事务。

### 3.2 Gateway

NewServer 校验依赖/Option 并安装 handler。BeforeStart 校验 App identity、准备内部 gRPC endpoint、Ping Locator、准备客户端 transport；失败按逆序回滚，每项使用独立 CleanupTimeout。准备完成后 Start 才开放准入并启动 transport。

Stop 顺序：

1. 关闭准入，等待认证及其同步 takeover Kick。
2. 停止 broadcaster，一次性交接本地全部 Session。
3. 有界 worker 在停止预算内 Kick、关闭连接并清理定位。
4. 停止客户端 transport、内部 gRPC 和 backend ClientConn，汇总错误。

### 3.3 Node

command/disconnect handler 在 BeforeStart 前注册。BeforeStart 校验 identity、endpoint、sticky/Locator 一致性并 Ping，Stateful Node 申请 epoch；失败由准备 owner 回滚。Start 先验证本地有效期并首次续租，再开放 gRPC；启动失败以独立 CleanupTimeout 调用完整 Stop。[实现](../node/lifecycle.go)

requests 跟踪入站请求和获准的 Registry 登记；deliveries 跟踪 Bind/Unbind/Push。Stop 顺序：

1. 拒绝并排空 Forward/Disconnect 等入站工作。
2. 执行业务 Drain；期间仍可绑定和推送，返回前业务须停止相关生产者。
3. 关闭出站准入，等待已接纳副作用，包括保存 Session 后发起的操作。
4. 停止 gRPC，按排空结果处理 epoch，最后关闭 Gateway ClientConn。

只有请求、Drain、出站均排空，才撤下可服务 identity，停止并等待续租，再按固定代次注销 epoch。任一排空失败则停止续租、保留 epoch 到TTL；只有已获准释放但尚未完成的注销可在后续 Stop 重试，NotFound/Conflict 视为本代已不再持有。Session 每次使用当前身份，正常停机后不可继续绑定或推送；摘流传播期间旧路由可收到 Unavailable，Gateway 不补偿重试。

### 3.4 Node epoch

`epochLease` 持有本代固定凭据、本地截止时间、续租与释放状态。identity 用于服务准入，lease 凭据还用于失败后的释放重试；两者不可合并。[实现](../node/epoch.go)

- TTL 30s，每10s续租；申请/单次续租最多3s，续租还受剩余租期限制。本地单调时钟从调用开始计算，迟到成功不能延长或恢复失效身份。
- 过期监视与续租 I/O 独立。普通错误只在租期内重试；到期或明确epoch缺失/冲突则关闭两类准入、取消已接纳工作并使 Start 返回 fatal。
- Forward（含空 sticky claim）、Disconnect、Session绑定及Push均核验有效期，binding查询后再检查。Stop 等待任务退出，超时保留凭据；不能强制终止忽略context的依赖或业务。
- 本地失效不撤销已完成业务写入，也不替代业务事务 fencing；fatal 不保证 Kratos 显式 Deregister，摘流仍依赖注册实现及进程退出。

## 4. 请求链路

### 4.1 认证与 Gate lease

OpAuth → Authenticator校验service/token并返回UID → BindGate写新binding并返回旧值 → 提交Session → 对不同旧binding同步best-effort Kick → OpAuthReply。认证不要求Node在线。

AuthTimeout 从Open起算，认证、BindGate和本地提交共享deadline。正常断开的Unbind与Disconnect共用独立CleanupTimeout，不继承已取消连接context或逐步重置预算；异常退出依赖TTL。heartbeat在本地剩余lease不超过LeaseTTL/2时续租。

### 4.2 业务请求

OpRequest → 校验Session/Gate lease → 读取service模式 → 定位/选择Node → gRPC Forward → Node校验service、本地租约及sticky claim → 注入Session、解码protobuf、middleware/handler → OpResponse。

已绑定Stateful请求由Gateway查binding与epoch，Node在handler前再次查binding并复核本地身份；改绑后的旧请求须拒绝，已开始业务不撤销。Gateway业务与心跳分别串行，关闭按业务锁、心跳锁、binding锁等待；远程Kick可在handler执行期间使路由失效，不能合并这些锁。[转发](../gateway/forward.go)、[分发](../node/dispatch.go)

typed middleware面对业务protobuf而非内部ForwardRequest，operation仍为`/cluster.v1.Node/Forward`。`CommandFromContext`提供实际`(int32, present)`，0是合法值；普通或nil context返回不存在，RawHandler可读取command但不自动应用typed middleware。

### 4.3 Push、Kick 与 Disconnect

- Session.Push 使用本次请求携带的GateRoute；PushToUID先定位当前Gate。旧Session不会自动转向重连后的新连接。
- 回程保留caller context与gRPC status；Gateway不可用为Unavailable，非法endpoint/TLS或不一致route为不泄漏地址的Internal。Gateway核验目标identity、完整binding、lease、MaxProtoSize；发送队列满为ResourceExhausted。
- 断开及Gateway正常停机best-effort调用Node.Disconnect；takeover Kick不发送该通知。Disconnect是at-most-once连接事件，可能晚于重连，不等于Logout；业务须在状态owner内比较BindingToken后更新状态。
- `node.ClientMiddleware`按配置顺序作用于两种Push的回程RPC；入站node.Middleware另有职责。应用可注入OTel并拥有provider/exporter，LocateGate、mailbox等待、桌fanout不在RPC计时范围内。
- Disconnect路由定位不额外查询epoch；不能与Forward合并为相同失败语义。支付、结算等副作用仍需业务幂等、事务或串行化。

## 5. Stateful 粘性路由

Node配置Locator即启用Stateful租约/绑定能力并声明`sticky=true`；Stateless不能仅为PushToUID配置Locator。Gateway首次使用service时固定sticky模式，endpoint/NodeID继续动态更新；模式变化或混合声明fail closed，Stateful/Stateless切换须重启全部Gateway。

| 情况 | 行为 |
| --- | --- |
| Stateless | service级WRR，不查Node Locator |
| Stateful未绑定 | 查询为空后WRR；handler可BindNode |
| Stateful已绑定 | 按NodeID精确投递；目标不可用、定位失败或binding变化时不改投 |
| 未绑定玩家并发首请求 | 可进入不同Node，有效进程间binding为last-write-wins |
| Gateway宕机 / 换Gateway重连 | Gate绑定按TTL回收或被新认证覆盖，Node绑定保留 |
| Node原ID重启 / 新ID重启 | 原ID取得新epoch可继承路由；新ID需业务改绑或等旧binding过期 |
| 同service/NodeID双活 | 后启动者epoch注册失败；不匹配claim返回Aborted |
| Redis不可用 | 依赖该次查询的请求失败；Node到本地租期截止时关闭准入 |

Gateway不缓存玩家binding或未绑定结果；close、takeover、heartbeat不修改Node binding。6h TTL只控制存储保留，安全业务保活尚缺；框架不保证强单活、内存状态恢复、同UID串行、actor唯一性或故障迁移。[修改权边界](#node-binding)

## 6. 协议、错误与默认值

### 6.1 外部协议

| Op | 方向 / 含义 |
| --- | --- |
| OpAuth / OpAuthReply | C→S / S→C；每连接一次认证 |
| OpHeartbeat / OpHeartbeatReply | C→S / S→C；存活与按需续租 |
| OpRequest / OpResponse | C→S / S→C；seq关联，cmd路由 |
| OpPush | S→C，无seq |
| OpKick | S→C，最后一帧后关闭，code说明原因 |

序列化Proto上限4096B。Proto.code是唯一框架状态，由gRPC status映射；业务状态放响应body。外部协议无deadline或取消帧，客户端截止时间不随消息进入服务端。[源定义](../api/protocol/v1/protocol.proto)

### 6.2 默认值

| 层 | 配置 | 默认值 |
| --- | --- | --- |
| Gateway | gRPC handler / RPCTimeout / AuthTimeout / LeaseTTL | 3s / 3s / 15s / 60s |
| Gateway | ConnectTimeout / LeaseTimeout / CleanupTimeout | 各3s |
| Gateway广播 | worker / queue | min(8, GOMAXPROCS) / 256 |
| Node | handler / PushTimeout / 启动回滚CleanupTimeout | 各3s |
| TCP Server | handler / handshake / heartbeat / write / send queue | 3s / 15s / 15s / 10s / 32 |
| TCP Client | ping / read / write / send queue | 5s / 15s / 10s / 100 |
| WS Server | handler / handshake / read / write / send queue | 3s / 15s / 60s / 10s / 32 |
| WS Client | ping | 15s |
| TCP/WS Server | HeartbeatHandler等待业务帧 | 8，不含当前请求 |
| TCP/WS Client | request / callback queue | 30s / 64 |
| 连接限制 | MaxConnLimit / MaxConnPerIP | 10,000 / 100 |

更短父deadline始终优先。TCP/WS可用Timeout、Node可用HandlerTimeout覆盖单次handler预算；AuthTimeout另管未认证总生命周期。TCP Client read须大于ping间隔；Client断开后由调用者新建实例，总连接/per-IP限制须分别核对。

### 6.3 请求预算与超时职责

沿同一context传播时取父deadline与本层上限的较早值，不仅在缺deadline时才补默认值。只放宽Gateway RPC仍可能被transport或Node截断；FIFO等待不计入实际调用起算的handler预算。

| 所有者 | 范围与边界 |
| --- | --- |
| TCP/WS Client Request | 限制本地等待；迟到响应按seq丢弃，不撤销已发送帧 |
| TCP/WS Invoker → Gateway Forward → Node handler | 默认分别3s；Forward覆盖定位与RPC，内部client不再叠加Kratos隐式超时；gRPC传播上游deadline |
| Gateway ConnectTimeout | 依赖准备与共享backend建连；调用者只控制自身等待 |
| Gateway LeaseTimeout | 单次Gate续租，仍服从heartbeat handler更短deadline |
| Gateway CleanupTimeout | 断线/Kick清理独立于caller取消；Session排空服从Stop总预算，启动回滚逐项独立计时 |
| Node PushTimeout | 每次LocateGate与回程RPC；业务同步多次Push各自计时 |
| Node CleanupTimeout | 准备/启动失败回滚；正常Stop使用调用方预算，epoch I/O另有3s上限 |
| Ludo入座 / 清理 | 5s / 独立2s，从Bind后的background context派生，不含创建玩家与Bind |
| Whot入座 / 清理 | 2s / 独立2s；保留入座与清理错误链 |

测试Gateway的`-rpc-timeout`同时配置WS handler和Gateway Forward，Node仍由自身配置决定。Ludo YAML handler为5s，历史压测为15s，不能混用容量结论。[运行配置](../test/README.md#本地启动)

业务mailbox未开始任务可取消，已开始任务必须等完成或业务自行回滚；多次同步Push可超过入座等待预算。`TryPost`只作有界准入，`Post`的context只限制等容量，已接纳的普通任务不随提交context取消。物理断连/Forward超时会取消在途Node context，但handler未返回前Node仍跟踪它，Stop先等请求再Drain；业务自启后台任务由Drain回收。

Redis caller deadline及错误分类尚未闭环，完整游戏、相同预算负载和长期SLO仍待验收，见 [I46](./issues.md#i46)。

## 7. 部署与安全约束

- Stateful Node ID稳定且在线唯一；Gateway、Node、Locator使用同一版本，不混部旧epoch/key或协议。
- Registry endpoint与GateBinding中的Gateway地址必须对调用方可达。内部TLS client/server成对启用，grpc/grpcs与配置一致；endpoint仅允许非零数字端口的host:port，不带userinfo/path/query/fragment。
- Server TLS提供Certificates、GetCertificate或GetConfigForClient之一，生产禁止InsecureSkipVerify。Option复制tls.Config顶层，共享证书元素、证书池和动态callback的并发安全由调用方负责。
- 认证/限流默认使用socket peer；代理真实IP须先定义信任边界。生产凭据、ACL/mTLS、限流和容量由部署验收，真实测试遵守 [隔离要求](./README.md#开发与验证)。
