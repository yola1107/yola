package nats

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"yola/event"
	"yola/internal/tlsconfig"

	natsgo "github.com/nats-io/nats.go"
)

var _ event.Bus = (*Bus)(nil)

// Bus owns one NATS connection and every subscription created through it.
type Bus struct {
	ctx             context.Context
	cancel          context.CancelFunc
	conn            *natsgo.Conn
	timeout         time.Duration
	queueCapacity   int
	maxPayloadBytes int

	registrationMu  sync.Mutex
	subscriptions   []*subscription
	registrationErr error

	closeOnce sync.Once
	closeDone chan struct{}
	closeErr  error
}

// New connects to NATS and returns a ready-to-use Bus. WithContext controls
// the Bus lifetime; canceling it closes the Bus and its subscriptions.
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
		closeDone:       make(chan struct{}),
	}
	if o.ctx.Done() != nil {
		go bus.closeOnContext(o.ctx)
	}
	return bus, nil
}

func validateOptions(o options) error {
	if o.timeout <= 0 {
		return errors.New("nats: timeout must be positive")
	}
	if o.queueCapacity <= 0 {
		return errors.New("nats: queue capacity must be positive")
	}
	if o.maxPayloadBytes <= 0 {
		return errors.New("nats: maximum payload size must be positive")
	}
	if o.tlsSet && o.tls == nil {
		return errors.New("nats: TLS configuration is required")
	}
	if o.url == "" {
		return errors.New("nats: URL is required")
	}
	if err := tlsconfig.ValidateClient(o.tls); err != nil {
		return fmt.Errorf("nats: %w", err)
	}
	return nil
}

func connect(ctx context.Context, o options) (*natsgo.Conn, error) {
	connectOptions := []natsgo.Option{
		natsgo.Timeout(timeoutFor(ctx, o.timeout)),
		natsgo.ReconnectBufSize(-1),
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
	// nats.Connect has no context-aware variant. The configured timeout bounds
	// this worker, which closes a late connection after caller cancellation.
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

// Close rejects new work, cancels subscriptions, waits for handlers, and closes NATS.
func (b *Bus) Close() error {
	b.closeOnce.Do(func() {
		defer close(b.closeDone)
		b.cancel()

		b.registrationMu.Lock()
		registered := b.subscriptions
		b.subscriptions = nil
		b.registrationMu.Unlock()

		for _, subscription := range registered {
			subscription.beginStop()
		}
		b.conn.Close()
		b.closeErr = waitSubscriptions(registered)
	})
	return b.closeErr
}

func (b *Bus) closeOnContext(ctx context.Context) {
	select {
	case <-ctx.Done():
		_ = b.Close()
	case <-b.closeDone:
	}
}

// Publish sends an online event without acknowledgement or replay guarantees.
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
	err := b.conn.Publish(e.Topic, e.Payload)
	if err != nil && b.ctx.Err() != nil {
		return event.ErrClosed
	}
	return err
}

// Subscribe activates one exact topic and starts its consumer. An activation
// failure stops further registration because Core NATS does not expose an
// error generation that can distinguish a repeated failure from stale state.
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
	previousConnectionErr := b.conn.LastError()
	registered := &subscription{
		topic:           topic,
		handler:         handler,
		maxPayloadBytes: b.maxPayloadBytes,
		stopDone:        make(chan struct{}),
	}
	messages, err := registered.activate(b.conn, b.queueCapacity)
	if err != nil {
		return nil, b.failRegistration(registered, fmt.Errorf("nats: subscribe %q: %w", topic, err))
	}
	if err := flush(activationCtx, b.conn, b.timeout); err != nil {
		if b.ctx.Err() != nil {
			err = event.ErrClosed
		}
		return nil, b.failRegistration(registered, fmt.Errorf("nats: subscribe %q: %w", topic, err))
	}
	if err := subscriptionActivationError(previousConnectionErr, b.conn.LastError()); err != nil {
		return nil, b.failRegistration(registered, fmt.Errorf("nats: subscribe %q: %w", topic, err))
	}
	if err := activationCtx.Err(); err != nil {
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
	b.registrationErr = errors.Join(cause, registered.stopErr)
	return b.registrationErr
}

// NATS wraps Publish and Subscription ACL failures with the same sentinel;
// only the latter belongs to this serialized activation window.
func subscriptionActivationError(previous, current error) error {
	if current == nil || previous != nil && current.Error() == previous.Error() {
		return nil
	}
	if errors.Is(current, natsgo.ErrMaxSubscriptionsExceeded) ||
		errors.Is(current, natsgo.ErrPermissionViolation) && strings.Contains(current.Error(), `Subscription to "`) {
		return current
	}
	return nil
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
