package nats

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"yola/event"

	natsgo "github.com/nats-io/nats.go"
)

var _ event.Bus = (*Bus)(nil)

// Bus 持有一个 NATS 连接、发布 worker 和通过它创建的全部订阅。
type Bus struct {
	ctx             context.Context
	cancel          context.CancelFunc
	conn            *natsgo.Conn
	timeout         time.Duration
	queueCapacity   int
	maxPayloadBytes int

	publishRequests chan publishRequest
	publishDone     chan struct{}

	registrationMu  sync.Mutex
	subscriptions   []*subscription
	registrationErr error

	closeOnce sync.Once
	closeErr  error
}

// New 连接 NATS 并返回可用的 Bus；WithContext 控制完整生命周期，
// 取消后关闭 Bus 及其订阅。
func New(opts ...Option) (*Bus, error) {
	o := options{
		ctx:             context.Background(),
		url:             natsgo.DefaultURL,
		timeout:         5 * time.Second,
		queueCapacity:   256,
		maxPayloadBytes: 64 << 10,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	if err := contextError(o.ctx); err != nil {
		return nil, err
	}
	if err := validateOptions(o); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(o.ctx)
	conn, err := connect(ctx, o)
	if err != nil {
		cancel()
		return nil, err
	}
	bus := &Bus{
		ctx:             ctx,
		cancel:          cancel,
		conn:            conn,
		timeout:         o.timeout,
		queueCapacity:   o.queueCapacity,
		maxPayloadBytes: o.maxPayloadBytes,
	}
	bus.startPublisher()
	if o.ctx.Done() != nil {
		go bus.closeOnContext()
	}
	return bus, nil
}

func connect(ctx context.Context, o options) (*natsgo.Conn, error) {
	connectOptions := []natsgo.Option{
		natsgo.Timeout(timeoutFor(ctx, o.timeout)),
		natsgo.ReconnectBufSize(-1),
		natsgo.ErrorHandler(func(_ *natsgo.Conn, sub *natsgo.Subscription, err error) {
			attrs := []slog.Attr{slog.Any("error", err)}
			if sub != nil {
				attrs = append(attrs, slog.String("topic", sub.Subject))
			}
			slog.LogAttrs(ctx, slog.LevelError, "event transport error", attrs...)
		}),
	}
	if o.username != "" || o.password != "" {
		connectOptions = append(connectOptions, natsgo.UserInfo(o.username, o.password))
	}
	if o.tls != nil {
		connectOptions = append(connectOptions, natsgo.Secure(o.tls))
	}
	type result struct {
		conn *natsgo.Conn
		err  error
	}
	// nats.Connect 不接受 context；配置的 timeout 限制此 worker 的等待，
	// caller 取消后，worker 负责关闭迟到的连接。
	connected := make(chan result)
	go func() {
		conn, err := natsgo.Connect(o.url, connectOptions...)
		select {
		case connected <- result{conn: conn, err: err}:
		case <-ctx.Done():
			if conn != nil {
				conn.Close()
			}
		}
	}()

	select {
	case outcome := <-connected:
		if err := ctx.Err(); err != nil {
			if outcome.conn != nil {
				outcome.conn.Close()
			}
			return nil, err
		}
		if outcome.err != nil {
			return nil, fmt.Errorf("nats: connect: %w", outcome.err)
		}
		return outcome.conn, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Close 拒绝新工作，取消订阅并关闭连接，等待发布 worker 和在途 handler 后返回。
func (b *Bus) Close() error {
	b.closeOnce.Do(func() {
		b.cancel()

		b.registrationMu.Lock()
		registered := b.subscriptions
		b.subscriptions = nil
		b.registrationMu.Unlock()

		for _, subscription := range registered {
			subscription.beginStop()
		}
		for _, subscription := range registered {
			<-subscription.deactivated
		}
		b.conn.Close()
		<-b.publishDone
		b.closeErr = waitSubscriptions(registered)
	})
	return b.closeErr
}

func (b *Bus) closeOnContext() {
	<-b.ctx.Done()
	_ = b.Close()
}

// Publish 发布在线事件，等待受 ctx 限制；成功不代表订阅者已收到。
// 返回后可复用 Payload。worker 通过取消检查后，发布尝试即已开始；
// 此后取消不能撤回发送，即使原生调用仍在等待锁，投递结果也可能不确定。
func (b *Bus) Publish(ctx context.Context, e event.Event) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if !event.ValidTopic(e.Topic) {
		return event.ErrInvalidTopic
	}
	if len(e.Payload) > b.maxPayloadBytes {
		return fmt.Errorf("nats: payload exceeds %d bytes", b.maxPayloadBytes)
	}
	if b.ctx.Err() != nil {
		return event.ErrClosed
	}
	// caller 取消后 worker 仍可能访问消息，交接前复制以隔离其可变切片。
	e.Payload = slices.Clone(e.Payload)
	request := publishRequest{ctx: ctx, event: e, result: make(chan error, 1)}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-b.ctx.Done():
		return event.ErrClosed
	case b.publishRequests <- request:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-b.ctx.Done():
		return event.ErrClosed
	case err := <-request.result:
		return err
	}
}

type publishRequest struct {
	ctx    context.Context
	event  event.Event
	result chan error
}

func (b *Bus) startPublisher() {
	// 无缓冲交接：最多执行一个原生 Publish，排队者仍由 caller 的 context 控制。
	b.publishRequests = make(chan publishRequest)
	b.publishDone = make(chan struct{})
	go b.publishLoop()
}

func (b *Bus) publishLoop() {
	defer close(b.publishDone)
	for {
		select {
		case <-b.ctx.Done():
			return
		case request := <-b.publishRequests:
			// select 可能同时选中取消与交接；此检查是发布尝试开始的边界。
			// 通过后与取消竞争的发送不保证撤回，包括随后的原生锁等待。
			if err := request.ctx.Err(); err != nil {
				request.result <- err
				continue
			}
			if b.ctx.Err() != nil {
				request.result <- event.ErrClosed
				return
			}
			err := b.conn.Publish(request.event.Topic, request.event.Payload)
			if err != nil && b.ctx.Err() != nil {
				err = event.ErrClosed
			}
			// caller 即使已取消也不会阻塞 worker；Close 会等待此 worker 退出。
			request.result <- err
		}
	}
}

// Subscribe 注册精确 Topic 并等待 Flush；成功不代表 broker 已接受订阅。
// ACL、订阅上限等异步错误独立记录；同步激活失败后停止后续注册。
func (b *Bus) Subscribe(ctx context.Context, topic string, handler event.Handler) (event.Subscription, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if !event.ValidTopic(topic) {
		return nil, event.ErrInvalidTopic
	}
	if handler == nil {
		return nil, event.ErrInvalidHandler
	}
	activationCtx, cancelActivation := context.WithCancel(ctx)
	stopLifetimeCancel := context.AfterFunc(b.ctx, cancelActivation)
	defer func() {
		stopLifetimeCancel()
		cancelActivation()
	}()

	b.registrationMu.Lock()
	defer b.registrationMu.Unlock()
	if b.ctx.Err() != nil {
		return nil, event.ErrClosed
	}
	if b.registrationErr != nil {
		return nil, fmt.Errorf("nats: subscription registration stopped after prior failure: %w", b.registrationErr)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	registered := &subscription{
		topic:           topic,
		handler:         handler,
		maxPayloadBytes: b.maxPayloadBytes,
		deactivated:     make(chan struct{}),
		stopDone:        make(chan struct{}),
	}
	messages, err := registered.activate(b.conn, b.queueCapacity)
	if err != nil {
		return nil, b.failRegistration(registered, fmt.Errorf("nats: subscribe %q: %w", topic, err))
	}
	err = flush(activationCtx, b.conn, b.timeout)
	if err == nil {
		err = activationCtx.Err()
	}
	if err != nil {
		if b.ctx.Err() != nil {
			err = event.ErrClosed
		}
		return nil, b.failRegistration(registered, fmt.Errorf("nats: subscribe %q: %w", topic, err))
	}
	registered.start(b.ctx, messages)
	// 只回收已结束且无错误的记录；取消中的 handler 和历史清理错误仍由 Close 等待、汇总。
	b.subscriptions = slices.DeleteFunc(b.subscriptions, func(previous *subscription) bool {
		select {
		case <-previous.stopDone:
			return previous.stopErr == nil
		default:
			return false
		}
	})
	b.subscriptions = append(b.subscriptions, registered)
	return registered, nil
}

func (b *Bus) failRegistration(registered *subscription, cause error) error {
	registered.beginStop()
	<-registered.stopDone
	b.registrationErr = errors.Join(cause, registered.stopErr)
	return b.registrationErr
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return event.ErrInvalidContext
	}
	return ctx.Err()
}

func flush(ctx context.Context, conn *natsgo.Conn, timeout time.Duration) error {
	flushCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return conn.FlushWithContext(flushCtx)
}

func timeoutFor(ctx context.Context, configured time.Duration) time.Duration {
	deadline, ok := ctx.Deadline()
	if !ok {
		return configured
	}
	remaining := time.Until(deadline)
	if remaining < configured {
		return max(remaining, time.Nanosecond)
	}
	return configured
}
