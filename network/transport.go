package network

import (
	"yola/network/internal/header"

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
	reqHeader   header.Carrier
	replyHeader header.Carrier
}

// NewTransport builds a transporter for ConnectionHandler invocations.
func NewTransport(kind transport.Kind, endpoint, remoteIP, connectionID string) *Transport {
	reqHeader := header.Carrier{}
	reqHeader.Set("remote_ip", remoteIP)
	reqHeader.Set(header.ConnectionIDKey, connectionID)
	return &Transport{
		kind:        kind,
		endpoint:    endpoint,
		reqHeader:   reqHeader,
		replyHeader: header.Carrier{},
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
