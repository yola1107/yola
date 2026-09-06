package nats

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"

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
	return messages, nil
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
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.ErrorContext(ctx, "event handler panic", "topic", received.Topic, "panic", recovered)
		}
	}()
	s.handler(ctx, received)
}

func (s *subscription) deactivate() (bool, error) {
	native := s.native
	cancel := s.cancel
	s.native = nil
	s.cancel = nil

	var err error
	if native != nil {
		if dropped, dropErr := native.Dropped(); dropErr == nil && dropped > 0 {
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
			s.handler = nil
			close(s.stopDone)
		}
	})
}

func (s *subscription) finish() {
	s.stopOnce.Do(func() {
		_, s.stopErr = s.deactivate()
	})
	s.handler = nil
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
