package network

import (
	"strings"

	"github.com/go-kratos/kratos/v3/transport"
)

var _ transport.Transporter = (*Transport)(nil)

// 长连接在 Kratos 中使用的 transport 类型。
const (
	KindTCP       transport.Kind = "tcp"
	KindWebSocket transport.Kind = "websocket"
)

// Transport 是长连接共用的 Kratos transporter。
type Transport struct {
	kind        transport.Kind
	endpoint    string
	reqHeader   headerCarrier
	replyHeader headerCarrier
}

// NewTransport 为 ConnectionHandler 调用构建 transporter。
func NewTransport(kind transport.Kind, endpoint, remoteIP, connectionID string) *Transport {
	reqHeader := make(headerCarrier)
	reqHeader.Set("remote_ip", remoteIP)
	reqHeader.Set("conn_id", connectionID)
	return &Transport{
		kind:        kind,
		endpoint:    endpoint,
		reqHeader:   reqHeader,
		replyHeader: make(headerCarrier),
	}
}

// Kind 返回 transport 类型。
func (tr *Transport) Kind() transport.Kind { return tr.kind }

// Endpoint 返回 transport endpoint。
func (tr *Transport) Endpoint() string { return tr.endpoint }

// Operation 返回 transport 操作。
func (tr *Transport) Operation() string { return ConnectionHandlerOperation }

// RequestHeader 返回请求 header。
func (tr *Transport) RequestHeader() transport.Header { return tr.reqHeader }

// ReplyHeader 返回响应 header。
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
