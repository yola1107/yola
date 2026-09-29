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
	_defaultAuthenticationTimeout = 3 * time.Second
	_defaultClientReadTimeout     = 15 * time.Second
	_defaultClientWriteTimeout    = 10 * time.Second
	_defaultCallbackQueueSize     = 64
)

// ClientOption 配置 TCP client。
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

// WithAddress 配置 Gateway TCP 地址。
func WithAddress(endpoint string) ClientOption {
	return func(o *clientOptions) {
		o.endpoint = endpoint
	}
}

// WithServiceName 配置目标 Node 服务。
func WithServiceName(serviceName string) ClientOption {
	return func(o *clientOptions) {
		o.serviceName = serviceName
	}
}

// WithToken 配置认证 token。
func WithToken(token string) ClientOption {
	return func(o *clientOptions) {
		o.token = []byte(token)
	}
}

// WithTLSConfig 配置 Gateway 连接的 TLS。
func WithTLSConfig(c *tls.Config) ClientOption {
	return func(o *clientOptions) {
		o.tlsConf = c
	}
}

// WithCodec 配置协议帧编码；通信双方必须使用相同 codec。
func WithCodec(codec encoding.Codec) ClientOption {
	return func(o *clientOptions) {
		o.codec = codec
	}
}

// WithPushHandler 配置服务端推送处理器。
func WithPushHandler(handlers map[int32]PushHandler) ClientOption {
	return func(o *clientOptions) {
		o.pushHandlers = handlers
	}
}

// WithKickHandler 配置主动断开连接的回调。
func WithKickHandler(handler KickHandler) ClientOption {
	return func(o *clientOptions) {
		o.kickHandler = handler
	}
}

// WithConnectFunc 配置认证成功后的连接回调。
func WithConnectFunc(fn func()) ClientOption {
	return func(o *clientOptions) {
		o.connectFunc = fn
	}
}

// WithDisconnectFunc 配置断线回调。
func WithDisconnectFunc(fn func()) ClientOption {
	return func(o *clientOptions) {
		o.disconnectFunc = fn
	}
}

// WithPingInterval 配置心跳间隔。
func WithPingInterval(interval time.Duration) ClientOption {
	return func(o *clientOptions) {
		o.pingInterval = interval
	}
}

// WithReadTimeout 配置认证后等待一帧入站消息的最长时间；
// 必须大于心跳间隔。
func WithReadTimeout(timeout time.Duration) ClientOption {
	return func(o *clientOptions) {
		o.readTimeout = timeout
	}
}

// WithWriteTimeout 配置认证后单帧写入的最长时间。
func WithWriteTimeout(timeout time.Duration) ClientOption {
	return func(o *clientOptions) {
		o.writeTimeout = timeout
	}
}

// WithRequestTimeout 配置请求等待结果的最长时间。
func WithRequestTimeout(timeout time.Duration) ClientOption {
	return func(o *clientOptions) {
		o.requestTimeout = timeout
	}
}

// WithCallbackQueueSize 配置等待执行的 connect/push 回调队列容量。
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
		readTimeout:       _defaultClientReadTimeout,
		writeTimeout:      _defaultClientWriteTimeout,
		requestTimeout:    30 * time.Second,
		callbackQueueSize: _defaultCallbackQueueSize,
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
