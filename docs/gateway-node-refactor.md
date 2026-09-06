# Gateway/Node 重构实施

本轮基线：`eeb99c4`。状态：六个入口已恢复原生 Kratos 装配，Node 租约重构保留，移除 App 封装后的验证和复审已完成。

## 实施结果

| 范围 | 当前结果 | 简化收益 |
| --- | --- | --- |
| 服务入口 | 三个 examples 入口和 test module 的 Gateway、Ludo、Whot 均直接使用 `kratos.New`；两个 Wire injector 返回 `*kratos.App` | 移除新增 App 类型、位置参数与 Option 混用、单元素 Server 列表以及额外构造错误分支；配置与 hooks 继续在入口显式表达 |
| Node epoch | 每次成功申请生成独立 `epochLease` | Server 删除续租 cancel、待清理凭据、重试标志和专用注销锁；lease 固定代次，不再在注销前后比较 Server 上可能变化的凭据 |

`epochLease` 只接收申请、续租、注销三项存储能力，通过失效回调通知 Server，不保留整个 Server。其注销状态为“持有 → 注销失败或已释放”；只有注销失败可由重复 Stop 重试。注销锁不再跨到 Server 的启动状态。[node/epoch.go:21](../node/epoch.go#L21)、[node/epoch.go:112](../node/epoch.go#L112)。

Gateway 的连接、认证和广播排空，以及 Node 的请求、Drain 和 epoch 释放仍保持各自顺序。六个入口和对应 Wire 源定义恢复后，通过已安装的 Wire 重新生成两个 `wire_gen.go`；未修改业务玩法、存储、配置和协议。

## 状态和失败契约

- BeforeStart 注册成功而提交失败时，准备 owner 把本次 lease 交给回滚流程；注销错误保留在该 lease 上，后续 Stop 可重试。
- 只有请求排空与业务 Drain 都成功，才撤下可服务 identity 并主动注销 lease。失败时取消续租，仍由 Redis TTL 回收。
- lease 的凭据在创建后固定，可服务 identity 仍单独撤销。保存了旧 Session 的调用方不会因重构获得永久有效的身份副本。
- 普通续租存储错误仍按原契约继续循环；NotFound/Conflict 才触发 lifecycle fatal。本轮没有添加本地 lease deadline，也没有删除 Gateway 的 epoch GET。

## 应用边界

Gateway/Node 自身初始化失败仍由各自 Server 回滚；Kratos 后续 Endpointer、启动 hook 或注册阶段失败，仍可能留下此前准备的 Server。移除额外 App 后，不再承诺跨 Server 的自动失败回收。当前服务入口启动失败后退出进程，listener 随进程释放，未注销的 epoch 等待 TTL。若需要进程继续运行并重建实例，须先明确完整回收流程；该项仍作为 [I42](./issues.md#运行与部署限制) 保留，不能视为已经修复。

Registry、Redis 和 EventBus 的关闭继续由创建方显式安排；Gateway/Node 不接管这些外部依赖。入口不增加新的 App、Runner、配置转交层或隐式 hooks。

## 验证记录

- 移除 App 后的格式化、gopls diagnostics、两个 module 的 `make check` 和 `make lint` 通过；新增和存量 lint 告警均为 0。
- 根 module、Ludo 和 Whot 的 `make build`，以及 test Gateway 的独立 `go build` 通过。六个入口、两个 Wire 源定义和重新生成的两个 injector 与基线无内容差异。
- Linux 执行 `go test -race -count=1 ./gateway ./node ./examples/gateway ./examples/ludo ./examples/whot` 通过，154 项顶层测试通过，无 data race 报告。本轮未启用真实 Redis/etcd，`TestGatewayNodeIntegration` 跳过。
- test module 的 `go test -race -count=1 ./gateway ./ludo/cmd/ludo-server ./whot/cmd/whot-server` 通过；这三个入口包没有测试文件，结果仅证明 race 构建成功。
- 保留 Node 的申请回滚、并发停止、注销失败重试、Drain/请求超时和 fencing 测试；`testing/synctest` 覆盖周期续租在取消后停止。
- Gateway WebSocket 集成测试等待两个实际心跳回复事件，再检查连接存活；测试间隔为 100ms，生产心跳实现和默认参数未变。[gateway/server_test.go:85](../gateway/server_test.go#L85)。

此前 Linux race 曾出现一次 WebSocket 连接提前关闭。原样基线的完整 race 三轮及基线、重构版各 50 次定向测试均未复现，尚不能确定该次失败根因。改为等待心跳事件后，上一轮含真实 Redis/etcd 的根 module race 三轮通过；本轮只运行上述相关包 race，没有重跑全量 race 或真实组件集成测试。

验证使用 Go 1.26.6、golangci-lint 2.13.2 和 gopls 0.21.1。Windows 使用 CGO_ENABLED=0，Linux race 使用 CGO_ENABLED=1；测试快照的 300 个源码/module 文件与本地逐一核对一致。

## 后续边界

将已绑定 Forward 从三次 Redis GET 减到两次，仍需先确认真实请求成本，并完整设计 Node 本地租约有效期、未绑定请求保护、在途业务副作用与实例身份发布。本轮不包含性能 benchmark、容量阶梯或真实业务 profile，没有性能提升结论。
