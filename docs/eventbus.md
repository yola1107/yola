# EventBus 接入

EventBus 提供在线、可丢失的 Pub/Sub。应用创建并关闭 Bus；Node usecase 依赖 Publisher，Gateway 由应用组装层接入订阅。

## 1. 职责边界

Topic 同时表示路由与事件名，Payload 是已编码的只读 bytes。Bus 不解释 protobuf/JSON，也不持有 Session、玩家或 Table。Gateway/Node 不拥有 Bus。

应用组装层定义客户端 Command，并固定映射 Topic → Command。Node 的有状态事件交给业务 mailbox；handler 不在接收协程内扫描连接或执行耗时存储操作。

## 2. 公共接口与接线

[接口定义](../event/event.go) 包含 Event、Handler、Publisher、Subscriber、Subscription 和 Bus。New 返回后即可 Publish/Subscribe，无需额外 Start/Stop。

在 Gateway 应用完成组件构造后注册在线公告；下例 `nats` 指 `yola/event/nats`，完整资源回收见 [Gateway 示例](../examples/gateway/main.go)：

```go
bus, err := nats.New(nats.WithURL(natsURL))
if err != nil {
	return err
}
defer bus.Close()

const announcementCommand int32 = 5
_, err = bus.Subscribe(context.Background(), "yola.gateway.announcement.v1",
	func(ctx context.Context, incoming event.Event) {
		if err := gate.Broadcast(announcementCommand, incoming.Payload); err != nil &&
			!errors.Is(err, gateway.ErrBroadcastQueueFull) {
			slog.WarnContext(ctx, "broadcast announcement", "error", err)
		}
	})
if err != nil {
	return err
}
```

发布者直接调用注入的 `event.Publisher.Publish(ctx, event.Event{Topic: ..., Payload: ...})`。每个在线 Bus 实例各收到一份，当前不支持 consumer group 或竞争消费。

## 3. 投递与取消语义

### 发布与消费

Publish 成功只表示 adapter 接受发送，不证明存在订阅者或 handler 已执行。无在线订阅、断线或本地队列满时允许丢失；不提供 ack、重试、重放或离线补发。

Bus 用单个 worker 串行调用原生 Publish，不另设发布等待队列。交接与结果等待均受 caller context 限制：

- 交接前取消，或 worker 执行前检查到取消时，不发送请求。
- worker 通过取消检查后即开始发布尝试。此后即使取消，消息仍可能投递，包括等待原生连接锁的情况；调用方不能据此安全重试或承诺撤回。

Publish 在交接前复制 Payload，返回后 caller 可复用原切片。每个等待交接的 caller 持有独立副本，因此应用须限制并发调用量。

同一订阅串行调用 handler。panic 被隔离并记录，消息按丢失处理；handler 不返回重投结果。

### 取消与关闭

`Unsubscribe(ctx)` 幂等发起停止：先取消 handler context，再由唯一清理任务执行原生 unsubscribe。caller 按 ctx 等待原生清理和消费协程退出。

等待超时返回 context 错误，并保留已发布的清理错误。后台停止继续，Bus 仍跟踪该订阅，调用方可再次等待。

`Bus.Close` 按以下顺序幂等关闭：

1. 拒绝新工作，唤醒等待的 Publish caller。
2. 取消全部订阅并启动清理，等待原生清理完成。
3. 关闭自有连接，等待发布 worker 和 handler 退出。

Close 没有 deadline，可能等待已开始的原生调用。handler 须响应取消，且不得同步调用自身 Unsubscribe 或 Bus.Close。

订阅完全退出后释放 handler 和队列引用。后续注册回收已完成且无错误的记录；带停止错误的记录保留到 Close 汇总。

## 4. NATS 生命周期

### 连接与注册

[NATS adapter](../event/nats/bus.go) 使用独占连接，并关闭 reconnect buffer。断线期间的 Publish 不会缓存后补发。

`WithContext` 的父 context 控制整个 Bus 生命周期，默认为 Background。父取消触发 Close；连接失败须创建新 Bus 重试。

Subscribe 注册精确 Topic，使用 `ChanSubscribe`、有界 channel，并等待 Flush。成功只表示本地装配和 Flush 完成，不证明 broker 接受订阅，也不等待全部异步错误回调结束。

| 失败时点 | 当前行为 |
| --- | --- |
| 参数校验失败或等待注册锁期间取消 | 不改变 Bus；等待期间取消的请求获锁后返回取消，不创建原生订阅 |
| 激活后订阅、Flush 或 context 失败 | 注册能力进入终态，后续 Subscribe 的错误保留首次失败；既有订阅与 Publish 继续，应用须关闭并重建 Bus |
| SUB/Publish ACL、订阅上限或 slow consumer 等异步错误 | slog Error 记录 `event transport error` 和原始 error；不直接终止后续注册，也不能用返回值判断权限通过 |

异步回调仅在携带订阅身份时记录 topic，不从错误文本推断归属。重复错误分别记录，回调可能晚于 Subscribe 返回。

被 broker 拒绝的本地订阅仍由 Bus/Subscription 回收。nats.go 重连时可能重新发送订阅，但不保证消息重放或成功恢复。生产认证、TLS 和 ACL 按 [I40](./issues.md#i40-nats-安全配置)验收。

### 容量限制

默认 `WithQueueCapacity=256`、`WithMaxPayloadBytes=64KiB`。后者限制 Publish 的 payload，并在接收出队后丢弃超限消息，无法限制已排队大消息的内存。

内存预算须使用 broker 实际的 max_payload，并计入订阅数、队列容量和运行时开销。计算示例与测量边界见 [NATS 容量口径](./performance.md#nats-容量口径)。

### 订阅观测

Subscription 可实现 `event.SubscriptionStatsProvider`。采样可并发执行，且不重置累计值：

| 字段 | 口径 |
| --- | --- |
| QueueDepth / QueueCapacity | 等待 channel 的条数和容量，不含当前 handler；退出后 depth 为 0 |
| QueueDropped / QueueDroppedCurrent | 原生本地队列满拒绝；Current=false 时仅保留最后可得值，关闭竞争期间的增量未知 |
| PayloadDropped | 超限消息出队后丢弃，不代表入队内存受限 |
| HandlerCalls / HandlerPanics / HandlerActive | 已结束调用数（含 panic）、其中 panic 数、当前执行状态 |
| HandlerDuration / LastHandlerDuration / MaxHandlerDuration | 已结束调用的累计、最近和最大耗时；不含等待与 panic 日志，不代表 p99 |
| Closed | 消费协程已退出，原句柄仍可读取累计值 |

字段为局部快照，不保证跨字段原子采样。原生订阅关闭后无法读取 Dropped；关闭回调不重入原生锁。

统计无法推断 broker、断线或关闭期间的消息丢失。业务 p99 须单独采样。

## 5. Gateway 在线 fanout

`Broadcast(command, payload)` 复制一次 Payload，并非阻塞地放入有界队列。队列满返回 `ErrBroadcastQueueFull`，成功只表示 Gateway 接纳。

单协调协程抓取 Session 快照，最多分成 BroadcastWorkers 批并行发送。全部完成后才处理下一条，保持连接所见顺序。默认 worker 为 `min(8, GOMAXPROCS)`，队列容量为 256；不按 Session 创建 goroutine。

广播只发送给已认证且 lease 有效的连接。支持 PreparedConnection 时共享默认 protobuf 编码；自定义 codec 仍逐连接编码。各连接的发送队列隔离慢消费者；队列满、连接关闭和停机均可丢弃消息，Gateway 限频记录 drop。

BroadcastStats 的观测边界：

- QueueDepth/QueueCapacity 是快照；Accepted/Completed/QueueDropped/SendDropped 从 Server 创建起累计。
- LastFanoutDuration/MaxFanoutDuration 只记录本地 fanout，不能证明客户端收到。
- 连接的 [SendStats](./architecture.md#发送观测) 与 SendDropped 可能记录同一次拒绝，不能跨层相加。
- 订阅、广播和连接队列分别在各自的接纳点拒绝消息。
