# EventBus 接入

EventBus 提供在线、可丢失的 Pub/Sub。应用负责创建与关闭 Bus，Gateway/Node 不持有它；Node usecase 只依赖 Publisher，Gateway 订阅由组装层接入。

## 1. 职责边界

Topic 同时表示路由与事件名，Payload 是已编码的只读 bytes。Bus 不解释 protobuf/JSON，不持有 Session、玩家或 Table，也不定义客户端 Command；Topic→Command 由 Gateway 的订阅组装层固定映射。Node 的有状态事件应交给业务 mailbox，handler 不在接收协程内扫描连接或执行耗时存储操作。

## 2. 公共接口与接线

[接口定义](../event/event.go) 包含 Event、Handler、Publisher、Subscriber、Subscription 和 Bus。New 返回后即可 Publish/Subscribe；没有第二套 Start/Stop。

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

发布者调用注入的 `event.Publisher.Publish(ctx, event.Event{Topic: ..., Payload: ...})`，不经 Gateway 中转，不伪造 Session。每个在线 Bus 实例各收到一份，当前没有 consumer group 或竞争消费。

## 3. 投递与取消语义

- Publish 成功只表示 adapter 接受发送，不证明存在订阅者或 handler 已执行。无在线订阅、断线或本地队列满时允许丢失；无 ack、重试、重放及离线补发。
- 同一订阅串行调用 handler，panic 被隔离并记录，消息仍视为丢失；handler 不返回重投结果。
- Unsubscribe(ctx) 幂等停止接收、取消 handler context 并等待在途返回。超时返回ctx.Err，后台停止继续，可再次等待；Bus仍跟踪取消中的订阅。
- Bus.Close 幂等拒绝新工作、取消并启动全部订阅停止、关闭自有连接，再等待handler退出。它没有deadline，handler必须协作响应取消；handler不得同步调用自身Unsubscribe或Bus.Close。
- 完全退出后释放handler和队列引用；后续注册回收已完成且无错误的记录，带停止错误的记录保留到Close汇总。

## 4. NATS 生命周期

[adapter实现](../event/nats/event.go) 使用一个独占连接，关闭reconnect buffer；断线期间Publish不会被缓存后延迟补发。WithContext的父context控制完整Bus生命周期，默认Background；连接失败需创建新Bus重试，父取消触发同一Close流程。

Subscribe注册精确Topic，底层ChanSubscribe加有界channel并等待Flush。返回成功仅表示本地装配/Flush完成，**不证明broker接受订阅**，也不是异步错误回调完成屏障。

| 失败时点 | 当前行为 |
| --- | --- |
| 参数校验、等待注册锁期间取消 | 不改变Bus；获锁后返回取消，不创建原生订阅 |
| 已进入激活后订阅/Flush/context失败 | 注册能力进入终态，后续Subscribe携带首次失败；既有订阅与Publish继续，应用关闭重建Bus |
| SUB/Publish ACL、订阅上限、slow consumer等异步错误 | slog Error记录`event transport error`及原始error；不直接终止后续注册，不能从返回值推断权限通过 |

异步回调只有携带订阅身份时才记录topic，不从错误文本猜测归属；重复错误分别记录，可能晚于Subscribe返回。被broker拒绝的本地订阅仍由Bus/Subscription回收，nats.go重连时可能重新发送订阅，不构成消息重放或成功恢复保证。生产认证/TLS/ACL须单独验收。[I40](./issues.md#i40)

容量默认值为WithQueueCapacity=256、WithMaxPayloadBytes=64KiB。后者限制Publish并在接收出队后丢弃超限Payload，**不能限制已排队的大消息内存**。预算按订阅数×队列容量×broker实际max_payload加运行时余量计算；broker同为64KiB时单订阅仅Payload约16MiB，放行1MiB时可达256MiB。配置须同时记录broker上限和订阅数，释放引用不保证RSS立即回落。[测量边界](./performance.md#nats-capacity)

Subscription可实现 `event.SubscriptionStatsProvider`，可并发采样，不重置累计值：

| 字段 | 口径 |
| --- | --- |
| QueueDepth / QueueCapacity | 等待channel的条数/容量，不含当前handler；退出后depth为0 |
| QueueDropped / QueueDroppedCurrent | 原生本地队列满拒绝；Current=false仅保留最后可得值，关闭竞争增量未知 |
| PayloadDropped | 超限消息出队后丢弃，不代表入队内存受限 |
| HandlerCalls / HandlerPanics / HandlerActive | 已结束调用（含panic）、其中panic数、当前执行状态 |
| HandlerDuration / LastHandlerDuration / MaxHandlerDuration | 已结束调用的累计、最近、最大耗时；不含等待和panic日志，不是p99 |
| Closed | 消费协程已退出，原句柄仍可读累计值 |

字段是局部快照，不承诺跨字段原子采样。原生订阅关闭后无法读取Dropped，不在关闭回调内重入原生锁；统计不推断broker/断线/关闭丢失，业务p99须另行采样。

## 5. Gateway 在线 fanout

`Broadcast(command, payload)` 复制一次Payload并非阻塞入有界队列，满时返回ErrBroadcastQueueFull；成功只表示Gateway接纳。单协调协程抓取Session快照，最多拆成BroadcastWorkers批并行发送，全部完成后才处理下一条，保持连接所见顺序。默认worker为min(8, GOMAXPROCS)、队列256；不为每个Session创建goroutine。

只发送给已认证且lease有效的连接。支持PreparedConnection时共享默认protobuf编码，自定义codec仍逐连接编码；各连接的发送队列隔离慢消费者，队列满、连接关闭和停机可丢弃，Gateway限频记录drop。

BroadcastStats的QueueDepth/QueueCapacity为快照，Accepted/Completed/QueueDropped/SendDropped从Server创建起累计；LastFanoutDuration/MaxFanoutDuration只描述本地fanout，不证明客户端收到。连接自身的 [SendStats](./architecture.md#send-stats) 与SendDropped可能记录同一次拒绝，不能跨层相加；订阅、广播、连接的队列拒绝分别发生在不同接纳点。
