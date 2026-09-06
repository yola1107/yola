package event

import (
	"context"
	"errors"
	"strings"
	"unicode"
)

var (
	ErrClosed         = errors.New("event: bus is closed")
	ErrInvalidContext = errors.New("event: context is nil")
	ErrInvalidTopic   = errors.New("event: topic is invalid")
	ErrInvalidHandler = errors.New("event: handler is nil")
)

// Event carries routing metadata and encoded business data.
type Event struct {
	Topic   string
	Payload []byte
}

// Handler processes an online event. It has no retry or acknowledgement result.
type Handler func(context.Context, Event)

// Publisher sends encoded business events without owning Bus lifecycle.
type Publisher interface {
	Publish(context.Context, Event) error
}

// Subscriber registers online event handlers.
type Subscriber interface {
	Subscribe(context.Context, string, Handler) (Subscription, error)
}

// Subscription owns one handler registration.
type Subscription interface {
	Unsubscribe(context.Context) error
}

// Bus is ready for online, best-effort event delivery when constructed.
// Close cancels all subscriptions, waits for running handlers, and releases
// resources owned by the Bus.
type Bus interface {
	Publisher
	Subscriber
	Close() error
}

// ValidTopic reports whether topic identifies one exact event stream.
func ValidTopic(topic string) bool {
	if topic == "" || strings.HasPrefix(topic, ".") || strings.HasSuffix(topic, ".") ||
		strings.Contains(topic, "..") || strings.ContainsAny(topic, "*>") {
		return false
	}
	for _, char := range topic {
		if unicode.IsSpace(char) || unicode.IsControl(char) {
			return false
		}
	}
	return true
}
