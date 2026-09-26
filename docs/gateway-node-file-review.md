# Gateway / Node：文件职责与清理结果

基线：`0c9043be299699a155fc1bd5ad5d3e310a43066c`。报告四项均已完成：前三项整理 gateway/node 的测试，第四项收拢 Node 租约失权操作。公共接入接口、协议和 `test/` 独立 module 源码未改。

## 已完成

| 项目 | 实际处理 | 当前代码 |
| --- | --- | --- |
| 启动夹具简化 | 删除单调用的 startTestNodeInstance；删除 sticky 参数及分支，直接使用 Node.Metadata | [test_helpers_test.go:390](../gateway/test_helpers_test.go#L390)、[参数化入口:396](../gateway/test_helpers_test.go#L396) |
| 失败路径回收 | listener、Node 和 App 都在可能失败之前登记 cleanup；健康探针使用 defer 关闭。正常停止等待、补偿 Node.Stop 各有独立 3s 预算，返回的 stop 保持幂等 | [test_helpers_test.go:399](../gateway/test_helpers_test.go#L399) |
| 认证专用夹具归位 | blockingBindLocator 的类型、构造与方法原样移至 auth_test.go，保留 Bind 完成后阻塞及先 unblock 再 Stop 的顺序 | [auth_test.go:365](../gateway/auth_test.go#L365) |
| 共享 Session 夹具归位 | savedBindingSession、callSessionBinding 原样移至 Node 共享 helper；场景继续控制自身 Stop/Drain | [test_helpers_test.go:287](../node/test_helpers_test.go#L287)、[绑定调用:304](../node/test_helpers_test.go#L304) |
| 建连超时去重 | backend 测试只保留 pool close 场景及 errBackendsClosed 身份断言；超时继续由完整配置链用例覆盖 | [backend_test.go:96](../gateway/backend_test.go#L96)、[timeout_test.go:100](../gateway/timeout_test.go#L100) |
| protobuf 解码去重 | 删除错误测试中仅为 malformed body 准备的 command 1；保留会核对 middleware 未执行的解码用例，以及 command 2/3/4 的错误覆盖 | [register_test.go:44](../node/register_test.go#L44)、[handler 错误:49](../node/register_test.go#L49) |
| 租约失权封装 | Session 通过 epochLease.loseOwnership 记录失权并取得首次取消原因，Server 继续负责准入和 fatal | [epoch.go:164](../node/epoch.go#L164)、[session.go:130](../node/session.go#L130) |

前三项修改 7 个已有测试文件，Go 代码净减 9 行。第四项修改 2 个生产文件、补充 2 个现有测试文件，生产净增 4 行、回归测试增加 35 行。合计 **11 个 Go 文件、净增 30 行**；文件数仍为 23 个生产文件、37 个测试文件。收益是减少转交、重复配置与覆盖，明确夹具和租约的职责；不以净行数下降作为封装收益。

E2E 的调用签名同步更新，原 **Deregister → 数据清理 → stopNode** 顺序保留。helper 提前注册的 cleanup 只作为托底；显式 stop 后重复执行仍安全。Node 的停止顺序、锁、租约有效期、存储调用和错误身份保持不变。

## 第四项：epochLease 的失权取消封装

[session.go:130](../node/session.go#L130) 的 handleBindingError 保留存储错误识别与对外状态映射，通过 [loseOwnership](../node/epoch.go#L164) 完成失权记录、取消本代工作并读取首次原因；不再直接读写 lease.cancel/ctx。Server 的 failLifecycle 仍负责关闭准入与发布 fatal，epochLease 不引用 Server。

两种错误语义分别保留：绑定路径需要首次记录的 context.Cause，续租路径返回本次 I/O 的包装错误。因此 renewOnce 的现有取消实现留在 epoch.go 内，不改成返回 loseOwnership 的结果。cancel 仍先于 failLifecycle，没有新增 goroutine、锁或取消等待屏障。

新增 [绑定失权保留已记录原因](../node/binding_fencing_test.go#L74) 和 [续租返回本次错误](../node/epoch_test.go#L333) 两条回归；既有 [迟到续租不能恢复失权](../node/binding_fencing_test.go#L123) 测试继续保护首次 cause 的对象身份。

## 文件设计与阅读路径

建议保留当前两层认知：先按职责读生产流程，再按验证主题进入测试。下面是**阅读分组，不是新增子目录方案**。

| 包 / 组 | 当前文件 | 先看什么 |
| --- | --- | --- |
| Gateway · 装配与生命期 | server.go、options.go、lifecycle.go | Server 持有哪些资源；NewServer 与 BeforeStart/Stop 的分工 |
| Gateway · 连接与认证 | inbound.go、auth.go、session.go | Handle 的操作分派 → 认证/心跳策略 → Session 锁和 binding 状态 |
| Gateway · Node 路由 | forward.go、backend.go、resolver.go、balancer.go | 一次 Forward 如何选 Node，再查看共享连接、发现更新、Ready picker |
| Gateway · 回程与广播 | cluster.go、broadcast.go | 单连接 Push/Kick 与有界广播分别由谁处理 |
| Node · 装配与生命期 | server.go、options.go、lifecycle.go、registry.go | 首次续租与就绪登记，requests → Drain → deliveries 的停止顺序 |
| Node · 请求执行 | cluster.go、dispatch.go、register.go | wire 入站 → 路由/身份校验 → 已注册的 typed adapter → handler |
| Node · 请求上下文与投递 | session.go、push.go | 捕获的 binding 与按 UID 重新定位是两种语义 |
| Node · 租约和准入 | epoch.go、admission.go | 本代凭据、截止时间、失权取消、在途工作追踪 |

两个最小阅读路线：

```text
读一次请求：
  gateway/inbound.Handle → forward.forward
  → node/cluster.Forward → dispatch.forwardTo
  → register.go 中生成的 typed adapter → 业务 handler

读停止：
  gateway/lifecycle.shutdown → 认证屏障/广播/Session 排空
  node/lifecycle.shutdown → requests → Drain → deliveries → epoch/连接回收
```

不建议继续拆 heartbeat.go、drain.go，或建立 routing/session/manager 子包。当前 Gateway 生命周期文件 386 行、Node 286 行，保留顺序有助核对资源回收。backend/resolver/balancer 分别负责连接、发现、Ready 选择，合在一个文件不能消除这三种状态。Node 的 register 与 registry 名称接近，但分别承担 command 适配与就绪登记；不能因名称相似而合并。

### 测试文件怎样读

| 包 | 验证主题 | 对应文件 |
| --- | --- | --- |
| Gateway | 装配、启动停止、预算 | server_test、options_test、lifecycle_test、lifecycle_app_test、timeout_test |
| Gateway | 认证与物理 Session | auth_test、session_test |
| Gateway | 路由与发现 | forward_test、backend_test、resolver_test、balancer_test |
| Gateway | 心跳与 FIFO | heartbeat_lifecycle_test、heartbeat_pipeline_test、heartbeat_benchmark_test |
| Gateway | 回程与广播 | cluster_test、broadcast_test、broadcast_benchmark_test |
| Gateway | 跨组件与外部依赖 | request_lifecycle_test、e2e_test |
| Gateway | 共享夹具 | test_helpers_test |
| Node | 装配、生命周期、登记、预算 | server_test、options_test、lifecycle_test、registry_test、registry_integration_test、timeout_test |
| Node | 入站、typed adapter、请求 context | cluster_test、dispatch_test、register_test、session_test |
| Node | 租约、绑定与排空竞争 | epoch_test、binding_fencing_test、binding_write_test、session_lifecycle_test、admission_test |
| Node | 出站投递 | push_test |
| Node | 共享夹具 | test_helpers_test |

表内测试文件均以 `.go` 结尾，合计 37 个。生命周期与白盒状态测试使用包内声明；将它们整体移进 test 子目录会改变 Go package 和可见范围。当前先修正夹具归属和装配义务，比为了目录外观改变测试 seam 更有依据。

## 验证

| 检查 | 本次结果 |
| --- | --- |
| 按 `.golangci.yml` 格式化、gopls 修改文件诊断 | 完成，无诊断 |
| 临时失败探针（前三项实施阶段） | 故意让 TLS Node 与明文健康探针不匹配，确认 helper 内部 Fatal 后监听端口可重新绑定；第四项未重跑该探针 |
| 临时 Stateful 探针（前三项实施阶段） | 确认模式由 Locator 推导、epoch 申请成功、显式 stop 后 epoch 注销、重复 stop 安全；第四项未重跑该探针 |
| `make check`（四项合并后） | 通过；gateway 47.329s、node 2.619s，含两条新回归。两个 module 的检查均执行，部分其他包命中 Go 缓存 |
| `make lint`（四项合并后） | 通过，两个 module 均为 0 issues |
| `go test -race ./node`（第四项） | 未通过；测试开始前因 Windows ThreadSanitizer 内存分配失败退出，error code 87。前三项的 Gateway/Node race 同样受此限制 |
| 完整任务 diff、文档引用、工作树与暂存区 diff 检查 | 已复审与检查 |

临时探针使用进程内 Redis 和本机随机端口，验证文件已删除。真实 Redis/etcd/Cluster 的集成环境未配置，对应测试跳过，不能记为外部验收通过。race 仍需在可正常启动检测器的环境补验。本次没有安装工具或修改依赖。
