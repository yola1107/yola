# EventBus 接入

当前只提供在线、可丢失的 Pub/Sub；不重试、不重放、不补发离线消息。

## 1. 职责边界

`event.Bus` 负责 Topic 路由、adapter 资源生命周期、发布、订阅和取消订阅，不解释 Payload，也不持有 Session、玩家或 Table 状态。

Gateway 拥有本地 Session 和连接发送队列，因此全量在线推送由 Gateway 自己并行 fanout；Node 的有状态事件由业务 handler 送入对应 mailbox。EventBus handler 应尽快把工作交给状态所有者，不能在 NATS 接收协程里扫描连接或访问 Redis/DB。

## 2. 公共接口

```go
package event

import "context"

type Event struct {
	Topic   string
	Payload []byte
}

type Handler func(context.Context, Event)

type Subscription interface {
	Unsubscribe(context.Context) error
}

type Publisher interface {
	Publish(context.Context, Event) error
}

type Subscriber interface {
	Subscribe(context.Context, string, Handler) (Subscription, error)
}

type Bus interface {
	Publisher
	Subscriber
	Close() error
}
```

- `Topic` 同时承担路由和事件名称，例如 `yola.gateway.announcement.v1`。当前没有第二套 `Type`。
- `Payload` 是调用方已经编码的只读 bytes；Bus 不接受 `any`，也不负责 protobuf/JSON 编解码。
- `Event` 不携带客户端 `Command`：Command 属于 Gateway 到客户端的协议，并由订阅组装层固定映射；发布者不能通过 EventBus 任意选择客户端消息号。
- Node usecase 只依赖 `Publisher`；应用组装层创建 Bus、注册订阅并关闭 Bus。
- `Handler` 不返回 error，因为当前没有 ack 或重投语义。业务失败由 handler 自己记录或交给业务状态所有者。
- `Subscribe` 只做广播订阅：每个在线 Bus 实例各收到一份。当前接口没有 consumer group 或竞争消费。
- `New` 成功返回的 Bus 已可 Publish/Subscribe，不再暴露第二套 Start/Stop 状态。

## 3. 投递与取消语义

公共语义是在线、best-effort、at-most-once：

- 没有在线订阅者、连接断开或本地有界队列已满时允许丢失。
- `Publish` 成功只表示 adapter 接受了发送，不表示存在订阅者或 handler 已执行。
- 不保存消费进度，不重试 handler，不补发离线期间的事件。
- 同一订阅由一个消费协程顺序调用 handler；EventBus 不提供公开 worker 数配置。
- handler panic 被隔离并记录，但当前消息仍视为丢失。

`Unsubscribe(ctx)` 幂等地停止新消息、取消 handler context，并等待正在执行的 handler 返回。超过期限时返回 `ctx.Err()`，但后台停止过程继续，后续调用可以再次等待。Bus 会持续跟踪正在取消的订阅，因此并发 `Bus.Close` 仍会等待该 handler。handler 不得同步调用自身的 `Unsubscribe` 或 `Bus.Close`，否则会等待自己返回。

`Bus.Close()` 幂等地拒绝新工作、取消所有订阅、等待正在运行的 handler 退出，并释放 Bus 独占的连接。它无 deadline，handler 必须响应自己的取消 context 并尽快返回。

订阅完全退出后释放 handler 及其捕获对象；后续成功注册会回收已经完成且无错误的订阅记录。取消中的订阅继续被跟踪，带停止错误的历史记录保留到 `Bus.Close()` 汇总，不因注册新订阅丢失错误。

## 4. NATS 生命周期

```text
New -> Publish/Subscribe/Unsubscribe -> Close
```

- `event/nats.New(...)` 校验配置并建立独占连接；未传 `WithContext` 时使用 `context.Background()`。传入的父 `ctx` 控制 Bus 的完整生命周期，取消后自动停止订阅并关闭连接；连接失败直接返回 error，重试时创建新 Bus。
- `Subscribe(ctx, ...)` 立即激活精确 Topic，底层使用 `ChanSubscribe` 和单个 bounded channel；Flush 和订阅 ACL/上限错误作为本次调用结果返回。
- 参数校验失败不会改变 Bus；进入底层激活后若订阅、Flush、context、订阅 ACL 或上限校验失败，本 Bus 的注册能力进入终态，后续 `Subscribe` 返回包含首次失败的 error。既有订阅和 Publish 继续运行，组装层应关闭并重建 Bus 后再重试。连接上的 slow-consumer 和 Publish ACL 等异步错误不会归因到新订阅。
- `Close` 取消 Bus context 和全部订阅、丢弃排队事件、等待运行中的 handler，并关闭 Bus 自己创建的连接；父 context 取消会触发相同关闭流程，之后仍可调用 `Close` 取得幂等的关闭结果。
- Bus 始终创建并独占一个连接，同时关闭 reconnect buffer；断线期间的 Publish 不会在重连后延迟补发。

NATS adapter 只暴露两个热路径容量参数：`WithQueueCapacity` 控制单订阅接收队列，`WithMaxPayloadBytes` 同时限制 Publish Payload 并丢弃超限的接收 Payload；默认分别为 256 和 64 KiB，按默认上限计算的单订阅 Payload 积压约为 16 MiB（不含结构和协议开销）。应用组装只需传入 NATS URL；生产值确有不同容量证据时再显式覆盖，并同步对齐 broker `max_payload`。

## 5. Gateway 在线 fanout

Gateway 只拥有本地 broadcaster，不拥有外部 Bus。Bus 构造后已可用，应用组装层直接注册订阅，并在 `App.Run` 返回后关闭。启动和停机窗口内的事件可能丢失，属于当前 best-effort 语义。

业务在应用组装阶段显式注册 Topic 到客户端 command 的映射：

```go
const announcementCommand int32 = 5 // 客户端协议约定的公告 Push 消息号
gate, err := gateway.NewServer(
	// Auth、Locator、Discovery 等其他依赖
)
if err != nil {
	return err
}
bus, err := eventnats.New(eventnats.WithURL(natsURL))
if err != nil {
	return err
}
defer bus.Close()

_, err = bus.Subscribe(
	context.Background(),
	"yola.gateway.announcement.v1",
	func(ctx context.Context, received event.Event) {
		if err := gate.Broadcast(announcementCommand, received.Payload); err != nil &&
			!errors.Is(err, gateway.ErrBroadcastQueueFull) {
			slog.WarnContext(ctx, "broadcast announcement", "error", err)
		}
	},
)
if err != nil {
	return err
}

app := kratos.New(
	kratos.BeforeStart(gate.BeforeStart),
	kratos.Server(gate),
)
```

订阅 handler 属于业务组装层：它固定 Topic 到客户端 command 的映射，只把 Payload 交给 `Broadcast`。Gateway 不依赖 `event`，EventBus 负责 Subscription 和 NATS 连接生命周期。

发布方直接使用组装层注入的 `event.Publisher`。Gateway/Node 不管理 Bus，也不为 Publish 增加透传 API；Node/usecase 不通过 Gateway 中转事件。

`Broadcast(command, payload)` 只复制一次 Payload，并把一条广播非阻塞写入 Gateway 的有界队列。返回成功表示本地 broadcaster 已接收，不表示每个连接都已入队。

fanout 流程：

1. 单个协调协程取得当前 Session 快照。
2. 一条广播最多拆成 `BroadcastWorkers` 个批次并行处理；默认 worker 数为 `min(8, GOMAXPROCS)`。
3. 全部批次完成后才处理下一条广播，避免同一连接上的后续广播越过前一条。
4. worker 只向已经认证且 lease 有效的连接调用 `SendProto`；连接自己的有界发送队列继续负责慢客户端隔离。
5. 广播队列默认容量为 256，可用 `BroadcastQueueCapacity` 调整；队列满返回 `gateway.ErrBroadcastQueueFull`。

Gateway 不为每个 Session 创建 goroutine，也不在 EventBus 内复制 Session 索引。连接发送队列满、连接关闭或停机过程中都允许丢弃，并以限频日志记录 drop。

`Gateway.BroadcastStats()` 返回一个不重置状态的瞬时快照：`QueueDepth` 是读取时的值，`QueueCapacity` 是配置容量，`Accepted` / `Completed` / `QueueDropped` / `SendDropped` 从 Server 创建起累计，`LastFanoutDuration` / `MaxFanoutDuration` 分别是最近一次和当前最大值。它只覆盖 Gateway 本地广播接纳与 fanout，不代表客户端已收到消息。

## 6. Node 接入

Node handler 只能调用 usecase、manager 或 mailbox，不得直接并发修改玩家/Table 状态，也不得伪造 request-scoped `node.Session`。

```go
type announcementService struct {
	events event.Publisher
}

func (s announcementService) Publish(ctx context.Context, payload []byte) error {
	return s.events.Publish(ctx, event.Event{
		Topic:   "yola.gateway.announcement.v1",
		Payload: payload,
	})
}
```

组装代码把 Bus 的 `event.Publisher` 能力传给业务 usecase，并由创建者 `defer Close`。`node.Session` 只表示当前请求对应的玩家路由，不能承载进程级事件发布；定时任务和后台 manager 也不依赖伪造 Session。
