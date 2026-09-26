# Gateway / Node 文件职责审查

2026-09-26，基线 `df8993002f939d46e0fc1915a7d127ce9a916cb6`。本次只审查和整理 `gateway/`、`node/`，不整理 network、其他框架包或 `test/` 独立模块。以下位置和行数以本次整理后的工作树为准。

依据为 [架构契约](./architecture.md)、本机安装的 [spf13/go-skills](https://github.com/spf13/go-skills) 中的职责内聚原则，以及实际声明、调用关系和相关测试。逐文件读取生产实现，以 gopls 核对拟迁移符号；测试文件核对测试入口、覆盖主题，并重点阅读受调整场景。没有把“文件越少越好”作为判断标准。

## 结论与实际改动

两个包都适合继续采用**单包、按职责分文件**。原有 25 个生产文件中，确认两处可以就地归并；其他文件大多对应独立的状态所有者、协议入口或执行阶段。

| 项目 | 查证依据 | 已完成处理 | 收益与边界 |
| --- | --- | --- | --- |
| Gateway 接管单独占文件 | `kickPrevious` 唯一生产调用来自 `authenticate` 的成功提交路径 | 原 `takeover.go` 合入 [auth.go:160](../gateway/auth.go#L160)，3 个接管测试并入 auth_test.go，删除原两文件 | 认证提交和旧连接接管在同一文件；保留函数、清理预算与调用顺序 |
| Gateway 心跳混在认证文件 | `Heartbeat` 入口在 inbound.go，续租实现和部分测试在 auth 文件 | 心跳、Gate 解绑和定位错误分类归入 [inbound.go:98](../gateway/inbound.go#L98)；对应测试移入已有 heartbeat_lifecycle_test.go | 连接事件与 Gate lease 维护集中；auth.go 专注认证和接管；未新增 heartbeat.go |
| Node command 元数据独占小文件 | commandKey 由 Forward 写入，CommandFromContext 与 FromContext 都读取请求 context | 原 command.go 合入 [session.go:47](../node/session.go#L47)，对应测试合入 session_test.go，删除原两文件 | 请求 Session 与 command 的 context 接口集中；键类型、公开函数和零值语义不变 |
| Node Endpoint 准备混在构造文件 | Endpoint 会准备 listener，失败时回收；BeforeStart 和 Stop 也操作同一资源 | 3 个 Endpoint/listener 方法由 server.go 移入 [lifecycle.go:19](../node/lifecycle.go#L19) | server.go 聚焦构造与身份；Endpoint 准备、启动、回滚和停止集中，和 Gateway 的布局一致 |

结果：Gateway 生产文件 **13 → 12**、测试文件 **21 → 20**；Node 生产文件 **12 → 11**、测试文件 **18 → 17**。共删除 **4 个 Go 文件**，净减 **34 行 Go 代码**，其中生产 14 行、测试 20 行，主要减少重复文件头和 import。没有删除算法、状态或测试用例，也没有性能收益声明。

## Gateway：全部生产文件

当前 12 个生产文件，2,564 行。重点是区分“连接事件”“连接状态”“Node 路由”和“组件生命周期”。

| 文件 / 行数 | 实际职责与主要协作 | 审查结论 |
| --- | --- | --- |
| [server.go:49](../gateway/server.go#L49) / 134 | Server、生命周期/准入状态、identity 的定义；构造内部 gRPC、Session 注册表、广播器与两种出站池 | 保留。是资源装配入口；不能把全部方法搬进来形成大文件 |
| [options.go:65](../gateway/options.go#L65) / 323 | 对外 Option、依赖接口、默认值、TLS/endpoint 校验与参数落地 | 保留。文件较长主要因为公开配置项，不是多个运行时职责混杂 |
| [lifecycle.go:54](../gateway/lifecycle.go#L54) / 386 | Endpoint、准备/回滚、Start/Stop、认证准入屏障、广播与连接排空、transport 回收 | 保留。能沿同一文件检查关闭顺序，不再新增 drain.go |
| [inbound.go:17](../gateway/inbound.go#L17) / 162 | network.ConnectionHandler 入口、认证/请求分派、独立心跳续租、Gate 解绑及错误分类 | 已收拢。认证和 Forward 仍委托专门实现；此文件不持有另一份 Session 状态 |
| [auth.go:20](../gateway/auth.go#L20) / 188 | 凭据校验、UID 规范性、BindGate、认证提交竞争、旧连接接管 | 已收拢。接管是本次认证提交的后续步骤，不再另占 takeover.go |
| [session.go:14](../gateway/session.go#L14) / 225 | 连接注册表、物理 Session、认证定时器、binding/lease deadline、受锁保护的发送与摘除 | 保留。注册表提交和连接移除需要共同审读，不能按结构体数量继续拆文件 |
| [forward.go:20](../gateway/forward.go#L20) / 171 | 业务 Forward、Stateless/Stateful 路由、binding/epoch 查询、错误映射、Disconnect 通知 | 保留。Disconnect 与 Forward 共用 Node 寻址，但前者不补查 epoch；不是重复请求逻辑 |
| [backend.go:24](../gateway/backend.go#L24) / 205 | 按 service 复用 ClientConn、共享建连、调用方等待、backendSnapshot 生命周期 | 保留。负责连接资源和发现快照的存放，不负责每次选择 Ready 连接 |
| [resolver.go:46](../gateway/resolver.go#L46) / 267 | Watch/重试、快照过滤、sticky/NodeID/endpoint 一致性、向 gRPC 发布地址 | 保留。拥有发现更新过程；不能与连接池或 picker 当成同一逻辑删除 |
| [balancer.go:39](../gateway/balancer.go#L39) / 91 | 将 Ready SubConn 建成 WRR 和精确 NodeID picker；维护 yola_wrr 注册 | 保留。小文件对应实际 gRPC 扩展点，发现实例与 Ready 连接不是同一集合 |
| [cluster.go:27](../gateway/cluster.go#L27) / 145 | 内部 Push/Kick RPC、参数和 binding 校验、定向发送/踢出、RPC 状态转换 | 保留。明确覆盖回程 RPC 及其执行，不要求只有一层参数转换 |
| [broadcast.go:48](../gateway/broadcast.go#L48) / 267 | 广播准入、worker/batch 顺序、PreparedProto 使用、排空及统计 | 保留。拥有独立运行状态；不是逐连接 Push 的简单循环替代品 |

认证阅读路径为 `inbound.Handle → auth.authenticate → sessionRegistry.commitAuthentication → auth.kickPrevious`。心跳则是 `inbound.Heartbeat → heartbeat → session.heartbeatRoute/finishHeartbeat`。因此心跳实现移动后，入口与续租策略同文件，锁和 lease 数据仍由 session.go 管理。

[session.go:91](../gateway/session.go#L91) 的 `handlerMu → heartbeatMu → bindingMu` 关闭锁顺序，以及注册表读锁对认证提交和 Close 的约束，说明 session.go 的两个私有类型应当放在一起。本次未改变任何锁、defer 或 I/O 时机。

## Node：全部生产文件

当前 11 个生产文件，1,526 行。Node 的职责复杂度主要来自请求、出站副作用和租约的不同生命周期，不能仅靠并文件消除。

| 文件 / 行数 | 实际职责与主要协作 | 审查结论 |
| --- | --- | --- |
| [server.go:37](../node/server.go#L37) / 126 | Server/identity 定义、gRPC 构造、回程 client 装配、Metadata、身份快照读取 | 已收拢。Endpoint/listener 操作移至生命周期文件；保留唯一资源入口 |
| [options.go:39](../node/options.go#L39) / 222 | 默认值、Option、endpoint/TLS、middleware、Locator、Drain 配置 | 保留。与 Gateway 部分形式相似，但配置契约和零值规则不同，不抽公共 BaseServer |
| [lifecycle.go:19](../node/lifecycle.go#L19) / 286 | Endpoint、准备与 epoch 申请交接、Start/就绪、失败回滚、Stop 与 epoch 释放决策 | 已收拢。统一资源准备/回收；不吞并 epochLease 的续租状态机 |
| [epoch.go:41](../node/epoch.go#L41) / 274 | 固定代次凭据、单调截止时间、独立续租/到期监视、工作取消、释放与重试状态 | 保留。是实际 owner，不应为少一个文件并进 Server；存在下述封装待改善点 |
| [admission.go:12](../node/admission.go#L12) / 60 | 已接纳工作计数、拒绝新工作、排空信号 | 保留。被 requests/deliveries 两个独立实例复用；合并两份状态会改变 Stop 阶段 |
| [registry.go:21](../node/registry.go#L21) / 61 | Kratos 登记就绪屏障、原启动错误、登记准入、身份/租约核验 | 保留。不是直接转发 Register；合入 register.go 反而混淆服务发现与 command 注册 |
| [register.go:21](../node/register.go#L21) / 92 | command 注册冻结、typed middleware、protobuf 编解码、返回值校验及错误适配 | 保留。unary 是实际 adapter，不是可删除的空转交层 |
| [cluster.go:25](../node/cluster.go#L25) / 68 | 内部 Forward/Disconnect RPC 入口；Forward 交给分发，Disconnect 在此完成专用执行 | 保留，明确其真实范围。当前没有必须统一成“纯协议适配文件”的契约 |
| [dispatch.go:19](../node/dispatch.go#L19) / 90 | Forward 准入、service/epoch/sticky claim 校验、command 查找与 context 注入 | 保留。相较 cluster.go，这里承载可独立核对的请求路由/分发规则 |
| [session.go:16](../node/session.go#L16) / 162 | 请求 Session 接口、Session/command context 元数据、绑定/解绑/当前连接 Push | 已合并 command.go。请求 metadata 接口集中；与 Gateway 物理 Session 的生命周期不同 |
| [push.go:16](../node/push.go#L16) / 85 | 按 UID 查当前 Gate、出站预算与准入、回程 Push、Locator/RPC 错误身份 | 保留。PushToUID 会重新定位，Session.Push 使用捕获的请求 binding，两种语义不能合并 |

[lifecycle.go:129](../node/lifecycle.go#L129) 的 requests → 业务 Drain → deliveries 顺序，有赖于上述不同 owner。`identity` 是服务准入快照，`epochLease.identity` 是失败后仍须保留的释放凭据，也不能当作重复字段删除。

## 对另一份分析的查证与取舍

| 建议 | 查证结果 | 本次处理 |
| --- | --- | --- |
| 租约失效更集中归 epochLease | 事实成立；Session 直接调用 lease.cancel 并读取 lease.ctx，续租路径也会记录失权 | 保留为明确的封装改善项；没有把状态迁移混入本次文件删除 |
| auth.go 拆出 heartbeat.go | 心跳混在认证文件的事实成立；新增文件不是唯一解决办法 | 已将心跳及 lease 维护归入现有 inbound.go，并迁移相关测试，文件总数减少 |
| Disconnect 执行移入 dispatch.go | 两个 RPC 的组织方式确实不同，但校验顺序和语义也不同 | 暂不增加单调用 dispatch helper；文件职责按实际实现说明，保留当前流程 |
| lifecycle.go 拆出 drain.go | 同文件同时编排 Stop 和实现连接排空，事实成立 | 保留。排空只服务 Stop，当前 386 行仍能顺序阅读；缺少需要独立拆分的维护证据 |

### 租约失效封装：有依据，但不属于死代码

[session.go:131](../node/session.go#L131) 的 `handleBindingError` 识别 epoch NotFound/Conflict 后直接操作 lease 的取消函数；[epoch.go:222](../node/epoch.go#L222) 在续租失权时也构造原因并取消。这使 Session 知道了租约的取消实现。

后续可以让 epochLease 提供“记录失权并取消本代工作”的操作，Server 保留关闭准入和发布 fatal 的职责。实施不能简单统一两个返回值：续租当前返回本次包装后的错误，绑定失权路径使用已记录的 `context.Cause`，并发下第一份取消原因可能已经确定。

还须保持源码调用顺序：先调用 `lease.cancel`，再调用 `failLifecycle`。`requestContext` 使用 `context.AfterFunc` 传播取消；这不等于先等待所有下游取消完成，再关闭准入，不能引入新的等待屏障。[epoch.go:164](../node/epoch.go#L164)

[binding_fencing_test.go:20](../node/binding_fencing_test.go#L20) 覆盖在途工作取消、普通错误不撤销租约、迟到续租不能恢复失权和 Drain 期间新 epoch 不被清理。本次完整保留这些测试；没有证据将现实现状称为运行 Bug。

### Forward / Disconnect：不同顺序确实存在

[dispatch.go:19](../node/dispatch.go#L19) 的 Forward 先准入，再检查 route/identity，并校验成对的 NodeID/epoch claim。即使 binding 无效，也先经过请求准入判断。

[cluster.go:42](../node/cluster.go#L42) 的 Disconnect 先校验 binding/identity，再准入，没有 Forward 的 sticky claim 和绑定重查。把两个 RPC 抽成共同执行入口容易改变错误优先级；只增加一个单调用函数搬走 Disconnect，也不会减少文件或消除现有算法重复。

## 测试文件逐项核对

测试文件占多数：整理前 64 个 Go 文件中有 39 个测试文件，整理后为 60 个文件、37 个测试文件。它们用于分开正常行为、并发竞争、跨组件链路和基准，不表示生产框架有同样多的模块。

### Gateway：20 个测试文件

| 文件 | 覆盖职责 | 处理 |
| --- | --- | --- |
| [auth_test.go](../gateway/auth_test.go) | 认证提交、deadline、失败清理与接管竞争 | 接收原 takeover 的 3 个用例，移出心跳/定位错误分类用例 |
| [backend_test.go](../gateway/backend_test.go) | service 连接复用、共享建连取消、TLS client 行为 | 保留 |
| [balancer_test.go](../gateway/balancer_test.go) | 精确 NodeID、等待 Ready、发现更新与 WRR | 保留，与 resolver 快照校验不同 |
| [resolver_test.go](../gateway/resolver_test.go) | 空快照、Watch 失败、节点过滤、sticky/endpoint 一致性 | 保留 |
| [forward_test.go](../gateway/forward_test.go) | 路由、定位错误、请求预算、回复与断线通知 | 保留 |
| [session_test.go](../gateway/session_test.go) | Session 路由、续租状态、发送、摘除和认证提交 | 保留，验证状态 owner |
| [cluster_test.go](../gateway/cluster_test.go) | Push/Kick 验证、大小限制、失效 binding 与清理 | 保留 |
| [broadcast_test.go](../gateway/broadcast_test.go) | 广播覆盖、prepared 发送、有界准入、停止与统计 | 保留 |
| [broadcast_benchmark_test.go](../gateway/broadcast_benchmark_test.go) | 广播准入、fanout 和 WS 编码成本 | 保留，与正确性测试分开 |
| [heartbeat_lifecycle_test.go](../gateway/heartbeat_lifecycle_test.go) | 慢 Forward 期间心跳、关闭等待续租、TTL/错误分类 | 接收原 auth 中的用例和专用 renewLeaseLocator |
| [heartbeat_pipeline_test.go](../gateway/heartbeat_pipeline_test.go) | wire 层认证屏障、业务 FIFO、过载与心跳独立处理 | 保留，与 lease 单测不是重复覆盖 |
| [heartbeat_benchmark_test.go](../gateway/heartbeat_benchmark_test.go) | Gate 续租失败波次成本 | 保留 |
| [lifecycle_test.go](../gateway/lifecycle_test.go) | 准备/回滚、transport 失败、Stop 与 Session 排空 | 保留 |
| [lifecycle_app_test.go](../gateway/lifecycle_app_test.go) | 外层 Kratos 启动失败时应用 owner 的资源回收 | 保留，测试的 owner 层级不同 |
| [request_lifecycle_test.go](../gateway/request_lifecycle_test.go) | Client 取消、Node Drain、重连和迟到清理的跨组件交互 | 保留独立 fixture，不并入简单 Forward 单测 |
| [options_test.go](../gateway/options_test.go) | 默认值、Option 错误、TLS/endpoint 与配置副本 | 保留 |
| [server_test.go](../gateway/server_test.go) | TCP/WS 接线、内部 TLS、SetHandler 失败 | 保留，构造装配职责明确 |
| [timeout_test.go](../gateway/timeout_test.go) | RPC/连接/续租/清理预算的隔离和上下游 deadline | 保留，共同验证时间预算而非单一方法 |
| [e2e_test.go](../gateway/e2e_test.go) | 显式真实 Redis/etcd 的 Gate/Node 集成 | 保留，与内存夹具验证分开 |
| [test_helpers_test.go](../gateway/test_helpers_test.go) | Discovery/Watcher、Locator、连接、TLS、App、backend 的共享夹具 | 保留；680 行较大，但不是生产抽象。未发现应直接删除的未使用声明，不再为夹具扩目录 |

原 `takeover_test.go` 已删除，测试函数名称、断言、并发控制和 cleanup 全部保留。

### Node：17 个测试文件

| 文件 | 覆盖职责 | 处理 |
| --- | --- | --- |
| [admission_test.go](../node/admission_test.go) | 准入终态、排空超时后仍跟踪在途工作 | 保留，与私有 owner 对应 |
| [epoch_test.go](../node/epoch_test.go) | epoch 获取/续租/失权、到期监视、迟到 I/O 与释放 | 保留，不并入通用 Server 测试 |
| [binding_fencing_test.go](../node/binding_fencing_test.go) | Bind/Unbind 发现失权后取消工作与禁止恢复 | 保留，与续租发现失权是不同入口 |
| [binding_write_test.go](../node/binding_write_test.go) | 延迟存储写在新 epoch 出现后不能覆盖新 binding | 保留，含显式真实 Redis 场景 |
| [cluster_test.go](../node/cluster_test.go) | Forward wire 参数和 Disconnect 回调 Session | 保留 |
| [dispatch_test.go](../node/dispatch_test.go) | Forward 参数、错误、同 UID 并发和错误 epoch | 保留，不将 wire 和分发断言误判为重复 |
| [register_test.go](../node/register_test.go) | typed middleware、handler 错误与注册冻结 | 保留 |
| [registry_test.go](../node/registry_test.go) | 初次续租就绪、原错误、登记失败回收与停止等待 | 保留 |
| [registry_integration_test.go](../node/registry_integration_test.go) | 原生 App 与真实依赖下的替代进程登记保护 | 保留，单独标识外部依赖 |
| [lifecycle_test.go](../node/lifecycle_test.go) | BeforeStart/Stop 交接、回滚、Drain 和 epoch 释放顺序 | 保留；707 行较长，但覆盖同一组件状态机，本次不新增拆分文件 |
| [options_test.go](../node/options_test.go) | 默认值、零 handler timeout、TLS、endpoint 与非法参数 | 保留 |
| [push_test.go](../node/push_test.go) | Session/UID Push、超时、Stop 期间投递、错误身份 | 保留，两个公开入口有不同路由语义 |
| [server_test.go](../node/server_test.go) | Metadata 与内部 TLS 接线 | 保留 |
| [session_test.go](../node/session_test.go) | 请求 Session、绑定/解绑、command/context 在真实 gRPC 和 middleware 中的传递 | 接收原 command 的两个用例 |
| [session_lifecycle_test.go](../node/session_lifecycle_test.go) | 保存后的 Session 与停止、Drain、失权的交互 | 保留，与即时请求 Session 用法不同 |
| [timeout_test.go](../node/timeout_test.go) | Push/准备回滚/正常 Stop 的预算隔离 | 保留 |
| [test_helpers_test.go](../node/test_helpers_test.go) | 内存 Locator、identity/Session、TLS、App 与等待夹具 | 保留，集中支撑上述状态测试 |

原 `command_test.go` 已删除，command=0、缺失 metadata、typed/raw middleware 及错误身份断言全部保留。

## 验证与剩余事项

| 检查 | 本次结果 |
| --- | --- |
| `golangci-lint fmt --config .golangci.yml <8 个修改文件>` | 完成 |
| gopls 修改文件诊断 | No diagnostics |
| 与基线 df89930 的 Go AST 声明内容比对 | 忽略 import、注释和格式，逐声明扫描 token；Gateway 473 / Node 289 组声明内容完全保留；测试/benchmark 入口分别为 115 / 94，均未增删 |
| `make check` | 通过；包含两个 module 的 tidy -diff、vet、staticcheck、普通测试及根协议 lint。受影响 gateway/node 测试本次实际运行，其他部分包命中 Go 缓存 |
| `make lint` | 通过；两个 module 均为 0 issues |
| `go test -race ./gateway ./node` | 未通过：两个测试进程均在测试开始前因 Windows ThreadSanitizer 分配内存失败退出，error code 87 |
| `GOMAXPROCS=4`，`go test -race -p 1 ./gateway ./node` | 用进程环境临时降低并行度后仍在相同初始化阶段失败；环境值已恢复，不能算 race 通过 |
| 完整任务 diff、文档链接与 `git diff --check` / cached check | 已复审与检查 |

真实 Redis/etcd/Cluster 的环境变量未配置，对应场景被跳过，未运行外部服务验收。本次没有安装/升级工具，也未修改 `AGENTS.md`、协议、依赖、公共 API、network 或 `test/` 源码。

剩余两点：race 需在能正常启动检测器的环境补验；租约失效的对象封装仍作为独立改进项保留。
