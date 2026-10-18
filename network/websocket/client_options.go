package websocket

import (
	"crypto/tls"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"strings"
	"time"

	"yola/internal/tlsconfig"

	"github.com/go-kratos/kratos/v3/encoding"
)

const (
	_defaultCallbackQueueSize = 64
	_webSocketScheme          = "ws"
	_secureWebSocketScheme    = "wss"
)

// ClientOption 配置 WebSocket client。
type ClientOption func(*clientOptions)

// WithEndpoint 配置客户端 endpoint。
func WithEndpoint(endpoint string) ClientOption {
	return func(o *clientOptions) {
		o.endpoint = endpoint
	}
}

// WithTimeout 配置建连和首次认证的超时。
func WithTimeout(timeout time.Duration) ClientOption {
	return func(o *clientOptions) {
		o.timeout = timeout
	}
}

// WithToken 配置认证 token。
func WithToken(token string) ClientOption {
	return func(o *clientOptions) {
		o.token = []byte(token)
	}
}

// WithServiceName 配置认证使用的 Node 服务。
func WithServiceName(service string) ClientOption {
	return func(o *clientOptions) {
		o.serviceName = service
	}
}

// WithTLSConfig 配置 TLS。
func WithTLSConfig(c *tls.Config) ClientOption {
	return func(o *clientOptions) {
		o.tlsConf = c
	}
}

// WithCodec 配置协议帧编码；通信双方必须使用相同 codec。
// 包内持有的默认 protobuf codec 不会被全局 codec 注册替换。
func WithCodec(codec encoding.Codec) ClientOption {
	return func(o *clientOptions) {
		o.codec = codec
	}
}

// WithChannelConfig 配置客户端 channel。
func WithChannelConfig(c *ChannelConfig) ClientOption {
	return func(o *clientOptions) {
		o.channel = c
	}
}

// WithPingInterval 配置客户端心跳间隔。
func WithPingInterval(interval time.Duration) ClientOption {
	return func(o *clientOptions) {
		o.pingInterval = interval
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

// WithConnectFunc 配置认证成功后的连接回调。
func WithConnectFunc(fn func(*Channel)) ClientOption {
	return func(o *clientOptions) {
		o.connectFunc = fn
	}
}

// WithDisconnectFunc 配置断线回调。
func WithDisconnectFunc(fn func(*Channel)) ClientOption {
	return func(o *clientOptions) {
		o.disconnectFunc = fn
	}
}

// WithPushHandler 配置推送处理器。
func WithPushHandler(handler map[int32]PushHandler) ClientOption {
	return func(o *clientOptions) {
		o.pushHandler = handler
	}
}

// WithKickHandler 配置主动断开连接的回调。
func WithKickHandler(handler KickHandler) ClientOption {
	return func(o *clientOptions) {
		o.kickHandler = handler
	}
}

type clientOptions struct {
	tlsConf           *tls.Config
	timeout           time.Duration
	endpoint          string
	serviceName       string
	token             []byte
	codec             encoding.Codec
	connectFunc       func(*Channel)
	disconnectFunc    func(*Channel)
	pushHandler       map[int32]PushHandler
	kickHandler       KickHandler
	channel           *ChannelConfig
	pingInterval      time.Duration
	requestTimeout    time.Duration
	callbackQueueSize int
}

func resolveClientOptions(opts ...ClientOption) (*clientOptions, error) {
	o := &clientOptions{
		endpoint:          "ws://127.0.0.1:3102",
		timeout:           3 * time.Second,
		codec:             defaultCodec(),
		pushHandler:       make(map[int32]PushHandler),
		channel:           defaultChannelConfig(),
		pingInterval:      DefaultPingInterval,
		requestTimeout:    DefaultRequestTimeout,
		callbackQueueSize: _defaultCallbackQueueSize,
	}
	for _, opt := range opts {
		opt(o)
	}
	if o.codec == nil {
		return nil, errors.New("websocket: codec is required")
	}
	if err := o.channel.validate(); err != nil {
		return nil, err
	}
	if o.timeout <= 0 {
		return nil, errors.New("websocket: authentication timeout must be positive")
	}
	if o.pingInterval <= 0 {
		return nil, errors.New("websocket: ping interval must be positive")
	}
	if o.pingInterval >= o.channel.ReadDeadline {
		return nil, errors.New("websocket: ping interval must be shorter than channel read deadline")
	}
	if o.requestTimeout <= 0 {
		return nil, errors.New("websocket: request timeout must be positive")
	}
	if o.serviceName == "" {
		return nil, errors.New("websocket: service name is required")
	}
	if len(o.token) == 0 {
		return nil, errors.New("websocket: token is required")
	}
	if o.callbackQueueSize <= 0 {
		return nil, errors.New("websocket: callback queue size must be positive")
	}
	if err := tlsconfig.ValidateClient(o.tlsConf); err != nil {
		return nil, fmt.Errorf("websocket: %w", err)
	}
	o.tlsConf = tlsconfig.Clone(o.tlsConf)
	channelConfig := *o.channel
	o.channel = &channelConfig
	o.pushHandler = maps.Clone(o.pushHandler)
	u, err := parseURL(o.endpoint, o.tlsConf == nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidURL, err)
	}
	if o.tlsConf != nil && u.Scheme != _secureWebSocketScheme {
		return nil, errors.New("websocket: TLS config requires a wss endpoint")
	}
	o.endpoint = u.String()
	return o, nil
}

func parseURL(endpoint string, insecure bool) (*url.URL, error) {
	if !strings.Contains(endpoint, "://") {
		if insecure {
			endpoint = _webSocketScheme + "://" + endpoint
		} else {
			endpoint = _secureWebSocketScheme + "://" + endpoint
		}
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.Scheme != _webSocketScheme && u.Scheme != _secureWebSocketScheme {
		return nil, errors.New("WebSocket endpoint must use ws or wss with a host")
	}
	return u, nil
}
