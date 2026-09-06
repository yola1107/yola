package tcp

import (
	"crypto/tls"
	"errors"
	"fmt"
	"maps"
	"time"

	"yola/internal/tlsconfig"

	"github.com/go-kratos/kratos/v3/encoding"
)

const (
	defaultAuthenticationTimeout = 3 * time.Second
	defaultClientReadTimeout     = 15 * time.Second
	defaultClientWriteTimeout    = 10 * time.Second
	defaultCallbackQueueSize     = 64
)

// ClientOption configures a TCP client.
type ClientOption func(*clientOptions)

type clientOptions struct {
	endpoint          string
	serviceName       string
	token             []byte
	tlsConf           *tls.Config
	codec             encoding.Codec
	pushHandlers      map[int32]PushHandler
	kickHandler       KickHandler
	disconnectFunc    func()
	connectFunc       func()
	pingInterval      time.Duration
	readTimeout       time.Duration
	writeTimeout      time.Duration
	requestTimeout    time.Duration
	callbackQueueSize int
}

// WithAddress configures the Gateway TCP address.
func WithAddress(endpoint string) ClientOption {
	return func(o *clientOptions) {
		o.endpoint = endpoint
	}
}

// WithServiceName configures the target Node service.
func WithServiceName(serviceName string) ClientOption {
	return func(o *clientOptions) {
		o.serviceName = serviceName
	}
}

// WithToken configures the authentication token.
func WithToken(token string) ClientOption {
	return func(o *clientOptions) {
		o.token = []byte(token)
	}
}

// WithTLSConfig configures TLS for the Gateway connection.
func WithTLSConfig(c *tls.Config) ClientOption {
	return func(o *clientOptions) {
		o.tlsConf = c
	}
}

// WithCodec configures protocol frame encoding. Peers must use the same codec.
func WithCodec(codec encoding.Codec) ClientOption {
	return func(o *clientOptions) {
		o.codec = codec
	}
}

// WithPushHandler configures server push handlers.
func WithPushHandler(handlers map[int32]PushHandler) ClientOption {
	return func(o *clientOptions) {
		o.pushHandlers = handlers
	}
}

// WithKickHandler configures the intentional disconnect callback.
func WithKickHandler(handler KickHandler) ClientOption {
	return func(o *clientOptions) {
		o.kickHandler = handler
	}
}

// WithConnectFunc configures the authenticated connection callback.
func WithConnectFunc(fn func()) ClientOption {
	return func(o *clientOptions) {
		o.connectFunc = fn
	}
}

// WithDisconnectFunc configures the disconnection callback.
func WithDisconnectFunc(fn func()) ClientOption {
	return func(o *clientOptions) {
		o.disconnectFunc = fn
	}
}

// WithPingInterval configures the heartbeat interval.
func WithPingInterval(interval time.Duration) ClientOption {
	return func(o *clientOptions) {
		o.pingInterval = interval
	}
}

// WithReadTimeout configures how long the authenticated connection may wait for one inbound frame.
// It must be longer than the heartbeat interval.
func WithReadTimeout(timeout time.Duration) ClientOption {
	return func(o *clientOptions) {
		o.readTimeout = timeout
	}
}

// WithWriteTimeout configures the maximum duration of one authenticated frame write.
func WithWriteTimeout(timeout time.Duration) ClientOption {
	return func(o *clientOptions) {
		o.writeTimeout = timeout
	}
}

// WithRequestTimeout configures how long a request remains pending.
func WithRequestTimeout(timeout time.Duration) ClientOption {
	return func(o *clientOptions) {
		o.requestTimeout = timeout
	}
}

// WithCallbackQueueSize configures the number of pending connect and push callbacks.
func WithCallbackQueueSize(size int) ClientOption {
	return func(o *clientOptions) {
		o.callbackQueueSize = size
	}
}

func resolveClientOptions(opts ...ClientOption) (*clientOptions, error) {
	o := &clientOptions{
		endpoint:          "127.0.0.1:3101",
		codec:             defaultCodec(),
		pushHandlers:      make(map[int32]PushHandler),
		pingInterval:      5 * time.Second,
		readTimeout:       defaultClientReadTimeout,
		writeTimeout:      defaultClientWriteTimeout,
		requestTimeout:    30 * time.Second,
		callbackQueueSize: defaultCallbackQueueSize,
	}
	for _, opt := range opts {
		opt(o)
	}
	if o.endpoint == "" {
		return nil, errors.New("tcp: endpoint is required")
	}
	if o.serviceName == "" {
		return nil, errors.New("tcp: service name is required")
	}
	if len(o.token) == 0 {
		return nil, errors.New("tcp: token is required")
	}
	if o.codec == nil {
		return nil, errors.New("tcp: codec is required")
	}
	if o.pingInterval <= 0 {
		return nil, errors.New("tcp: ping interval must be positive")
	}
	if o.readTimeout <= 0 {
		return nil, errors.New("tcp: read timeout must be positive")
	}
	if o.readTimeout <= o.pingInterval {
		return nil, errors.New("tcp: read timeout must exceed ping interval")
	}
	if o.writeTimeout <= 0 {
		return nil, errors.New("tcp: write timeout must be positive")
	}
	if o.requestTimeout <= 0 {
		return nil, errors.New("tcp: request timeout must be positive")
	}
	if o.callbackQueueSize <= 0 {
		return nil, errors.New("tcp: callback queue size must be positive")
	}
	if err := tlsconfig.ValidateClient(o.tlsConf); err != nil {
		return nil, fmt.Errorf("tcp: %w", err)
	}
	o.tlsConf = tlsconfig.Clone(o.tlsConf)
	o.pushHandlers = maps.Clone(o.pushHandlers)
	return o, nil
}
