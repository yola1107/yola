package nats

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"yola/event"
	"yola/internal/contextwait"

	natsgo "github.com/nats-io/nats.go"
)

type subscription struct {
	topic           string
	handler         event.Handler
	maxPayloadBytes int
	native          *natsgo.Subscription
	cancel          context.CancelFunc
	statsMu         sync.Mutex
	stats           event.SubscriptionStats
	messages        <-chan *natsgo.Msg
	dropped         atomic.Uint64
	stopOnce        sync.Once
	stopDone        chan struct{}
	stopErr         error
}

func (s *subscription) Unsubscribe(ctx context.Context) error {
	if ctx == nil {
		return event.ErrInvalidContext
	}
	s.beginStop()
	return s.wait(ctx)
}

func (s *subscription) activate(conn *natsgo.Conn, queueCapacity int) (<-chan *natsgo.Msg, error) {
	messages := make(chan *natsgo.Msg, queueCapacity)
	native, err := conn.ChanSubscribe(s.topic, messages)
	if err != nil {
		return nil, err
	}

	s.native = native
	s.messages = messages
	s.stats.QueueCapacity = queueCapacity
	return messages, nil
}

// SubscriptionStats 返回本地订阅快照；原生 drop 失效后保留最后可得值。
func (s *subscription) SubscriptionStats() event.SubscriptionStats {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	s.sampleDropped()
	stats := s.stats
	stats.QueueDepth = len(s.messages)
	stats.PayloadDropped = s.dropped.Load()
	return stats
}

// sampleDropped 由 statsMu 保护；不得从原生订阅关闭回调重入原生锁。
func (s *subscription) sampleDropped() {
	s.stats.QueueDroppedCurrent = false
	if s.native == nil {
		return
	}
	if dropped, err := s.native.Dropped(); err == nil {
		s.stats.QueueDropped = uint64(dropped)
		s.stats.QueueDroppedCurrent = true
	}
}

func (s *subscription) start(parent context.Context, messages <-chan *natsgo.Msg) {
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	go func() {
		s.consume(ctx, messages)
		s.finish()
	}()
}

func (s *subscription) consume(ctx context.Context, messages <-chan *natsgo.Msg) {
	for {
		select {
		case <-ctx.Done():
			return
		case message, ok := <-messages:
			if !ok {
				return
			}
			if ctx.Err() != nil {
				return
			}
			s.handle(ctx, message)
		}
	}
}

func (s *subscription) handle(ctx context.Context, message *natsgo.Msg) {
	if len(message.Data) > s.maxPayloadBytes {
		s.recordDrop("payload_too_large")
		return
	}
	received := event.Event{
		Topic: message.Subject,
		// nats.go owns this buffer and does not reuse it after delivery.
		Payload: message.Data,
	}
	s.statsMu.Lock()
	s.stats.HandlerActive = true
	s.statsMu.Unlock()
	started := time.Now()
	defer func() {
		recovered := recover()
		duration := time.Since(started)
		s.statsMu.Lock()
		s.stats.HandlerActive = false
		s.stats.HandlerCalls++
		s.stats.HandlerDuration += duration
		s.stats.LastHandlerDuration = duration
		s.stats.MaxHandlerDuration = max(s.stats.MaxHandlerDuration, duration)
		if recovered != nil {
			s.stats.HandlerPanics++
		}
		s.statsMu.Unlock()
		if recovered != nil {
			slog.ErrorContext(ctx, "event handler panic", "topic", received.Topic, "panic", recovered)
		}
	}()
	s.handler(ctx, received)
}

func (s *subscription) deactivate() (bool, error) {
	s.statsMu.Lock()
	s.sampleDropped()
	native := s.native
	cancel := s.cancel
	s.native = nil
	s.cancel = nil
	dropped := s.stats.QueueDropped
	s.stats.QueueDroppedCurrent = false
	s.statsMu.Unlock()

	var err error
	if native != nil {
		if dropped > 0 {
			slog.Warn("event dropped", "topic", s.topic, "reason", "queue_full", "dropped", dropped)
		}
		err = native.Unsubscribe()
	}
	if cancel != nil {
		cancel()
	}
	return cancel != nil, err
}

func (s *subscription) beginStop() {
	s.stopOnce.Do(func() {
		started, err := s.deactivate()
		s.stopErr = err
		if !started {
			s.complete()
		}
	})
}

func (s *subscription) finish() {
	s.stopOnce.Do(func() {
		_, s.stopErr = s.deactivate()
	})
	s.complete()
}

func (s *subscription) complete() {
	s.handler = nil
	s.statsMu.Lock()
	s.messages = nil
	s.stats.Closed = true
	s.statsMu.Unlock()
	close(s.stopDone)
}

func (s *subscription) wait(ctx context.Context) error {
	if err := contextwait.Done(ctx, s.stopDone); err != nil {
		return errors.Join(s.stopErr, err)
	}
	return s.stopErr
}

func (s *subscription) recordDrop(reason string) {
	dropped := s.dropped.Add(1)
	if dropped&(dropped-1) == 0 {
		slog.Warn("event dropped", "topic", s.topic, "reason", reason, "dropped", dropped)
	}
}

func waitSubscriptions(subscriptions []*subscription) error {
	errs := make([]error, 0, len(subscriptions))
	for _, registered := range subscriptions {
		<-registered.stopDone
		errs = append(errs, registered.stopErr)
	}
	return errors.Join(errs...)
}
