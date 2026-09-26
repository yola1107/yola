package network

import (
	"strings"

	"github.com/go-kratos/kratos/v3/transport"
)

var _ transport.Transporter = (*Transport)(nil)

const (
	// KindTCP identifies the TCP transport.
	KindTCP transport.Kind = "tcp"
	// KindWebSocket identifies the WebSocket transport.
	KindWebSocket transport.Kind = "websocket"
)

// Transport is the shared Kratos transporter for long-lived client connections.
type Transport struct {
	kind        transport.Kind
	endpoint    string
	reqHeader   headerCarrier
	replyHeader headerCarrier
}

// NewTransport builds a transporter for ConnectionHandler invocations.
func NewTransport(kind transport.Kind, endpoint, remoteIP, connectionID string) *Transport {
	reqHeader := headerCarrier{}
	reqHeader.Set("remote_ip", remoteIP)
	reqHeader.Set("conn_id", connectionID)
	return &Transport{
		kind:        kind,
		endpoint:    endpoint,
		reqHeader:   reqHeader,
		replyHeader: headerCarrier{},
	}
}

// Kind returns the transport kind.
func (tr *Transport) Kind() transport.Kind { return tr.kind }

// Endpoint returns the transport endpoint.
func (tr *Transport) Endpoint() string { return tr.endpoint }

// Operation returns the transport operation.
func (tr *Transport) Operation() string { return ConnectionHandlerOperation }

// RequestHeader returns the request header.
func (tr *Transport) RequestHeader() transport.Header { return tr.reqHeader }

// ReplyHeader returns the reply header.
func (tr *Transport) ReplyHeader() transport.Header { return tr.replyHeader }

// headerCarrier 按小写键保存多值 header。
type headerCarrier map[string][]string

func (c headerCarrier) Get(key string) string {
	values := c.Values(key)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func (c headerCarrier) Set(key, value string) {
	c[strings.ToLower(key)] = []string{value}
}

func (c headerCarrier) Add(key, value string) {
	key = strings.ToLower(key)
	c[key] = append(c[key], value)
}

func (c headerCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for key := range c {
		keys = append(keys, key)
	}
	return keys
}

func (c headerCarrier) Values(key string) []string {
	return c[strings.ToLower(key)]
}
