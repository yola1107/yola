package nats

import (
	"context"
	"crypto/tls"
	"net/url"
	"time"

	"yola/internal/tlsconfig"
)

type Option func(*options)

type options struct {
	ctx             context.Context
	url             string
	timeout         time.Duration
	username        string
	password        string
	tls             *tls.Config
	tlsSet          bool
	queueCapacity   int
	maxPayloadBytes int
}

// WithContext controls the Bus lifetime, including the initial connection.
func WithContext(ctx context.Context) Option {
	return func(o *options) { o.ctx = ctx }
}

func WithURL(url string) Option {
	return func(o *options) { o.url = url }
}

func WithTimeout(timeout time.Duration) Option {
	return func(o *options) { o.timeout = timeout }
}

func WithQueueCapacity(capacity int) Option {
	return func(o *options) { o.queueCapacity = capacity }
}

func WithMaxPayloadBytes(size int) Option {
	return func(o *options) { o.maxPayloadBytes = size }
}

func WithUserInfo(username, password string) Option {
	return func(o *options) {
		o.username = username
		o.password = password
	}
}

func WithTLS(config *tls.Config) Option {
	return func(o *options) {
		o.tlsSet = true
		o.tls = tlsconfig.Clone(config)
	}
}

func RedactedURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" {
		return "nats://"
	}
	parsed.User = nil
	return parsed.String()
}
