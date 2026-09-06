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
	defaultCallbackQueueSize = 64
	webSocketScheme          = "ws"
	secureWebSocketScheme    = "wss"
)

// ClientOption configures a WebSocket client.
type ClientOption func(*clientOptions)

// WithEndpoint configures the client endpoint.
func WithEndpoint(endpoint string) ClientOption {
	return func(o *clientOptions) {
		o.endpoint = endpoint
	}
}

// WithTimeout configures the connection and initial authentication timeout.
func WithTimeout(timeout time.Duration) ClientOption {
	return func(o *clientOptions) {
		o.timeout = timeout
	}
}

// WithToken configures the authentication token.
func WithToken(token string) ClientOption {
	return func(o *clientOptions) {
		o.token = []byte(token)
	}
}

// WithServiceName configures the Node service used for authentication.
func WithServiceName(service string) ClientOption {
	return func(o *clientOptions) {
		o.serviceName = service
	}
}

// WithTLSConfig configures TLS.
func WithTLSConfig(c *tls.Config) ClientOption {
	return func(o *clientOptions) {
		o.tlsConf = c
	}
}

// WithCodec configures protocol frame encoding. Peers must use the same codec.
// The package-owned protobuf default is not replaced by global codec registration.
func WithCodec(codec encoding.Codec) ClientOption {
	return func(o *clientOptions) {
		o.codec = codec
	}
}

// WithChannelConfig configures the client channel.
func WithChannelConfig(c *ChannelConfig) ClientOption {
	return func(o *clientOptions) {
		o.channel = c
	}
}

// WithPingInterval configures the client heartbeat interval.
func WithPingInterval(interval time.Duration) ClientOption {
	return func(o *clientOptions) {
		o.pingInterval = interval
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

// WithConnectFunc configures the authenticated connection callback.
func WithConnectFunc(fn func(*Channel)) ClientOption {
	return func(o *clientOptions) {
		o.connectFunc = fn
	}
}

// WithDisconnectFunc configures the disconnection callback.
func WithDisconnectFunc(fn func(*Channel)) ClientOption {
	return func(o *clientOptions) {
		o.disconnectFunc = fn
	}
}

// WithPushHandler configures push handlers.
func WithPushHandler(handler map[int32]PushHandler) ClientOption {
	return func(o *clientOptions) {
		o.pushHandler = handler
	}
}

// WithKickHandler configures the intentional disconnect callback.
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
		callbackQueueSize: defaultCallbackQueueSize,
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
	if o.tlsConf != nil && u.Scheme != secureWebSocketScheme {
		return nil, errors.New("websocket: TLS config requires a wss endpoint")
	}
	o.endpoint = u.String()
	return o, nil
}

func parseURL(endpoint string, insecure bool) (*url.URL, error) {
	if !strings.Contains(endpoint, "://") {
		if insecure {
			endpoint = webSocketScheme + "://" + endpoint
		} else {
			endpoint = secureWebSocketScheme + "://" + endpoint
		}
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.Scheme != webSocketScheme && u.Scheme != secureWebSocketScheme {
		return nil, errors.New("WebSocket endpoint must use ws or wss with a host")
	}
	return u, nil
}
