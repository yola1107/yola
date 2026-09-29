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

// Event 携带路由信息和已编码的业务数据。
type Event struct {
	Topic   string
	Payload []byte
}

// Handler 处理在线事件，不返回重试或确认结果。
type Handler func(context.Context, Event)

// Publisher 发布已编码的业务事件，不拥有 Bus 的生命周期。
type Publisher interface {
	Publish(context.Context, Event) error
}

// Subscriber 注册在线事件处理器。
type Subscriber interface {
	Subscribe(context.Context, string, Handler) (Subscription, error)
}

// Subscription 持有一次 handler 注册。
type Subscription interface {
	Unsubscribe(context.Context) error
}

// Bus 构造成功后即可提供尽力投递的在线事件服务。
// Close 取消全部订阅，等待在途 handler，并释放 Bus 自有资源。
type Bus interface {
	Publisher
	Subscriber
	Close() error
}

// ValidTopic 判断 topic 是否表示一个精确事件流。
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
