package network

import (
	"context"
	"errors"

	"yola/api/protocol/v1"
)

var (
	// ErrConnectionClosed indicates that a connection can no longer accept messages.
	ErrConnectionClosed = errors.New("network: connection closed")
	// ErrSendQueueFull indicates that a connection cannot accept another queued message.
	ErrSendQueueFull = errors.New("network: send queue full")
	// ErrFrameTooLarge indicates that a serialized protocol frame exceeds the transport limit.
	ErrFrameTooLarge = errors.New("network: frame too large")
)

// ConnectionHandlerOperation identifies transport calls into ConnectionHandler.Handle.
const ConnectionHandlerOperation = "/network.ConnectionHandler/Handle"

// Connection is one external client connection owned by a transport server.
type Connection interface {
	ConnID() string
	RemoteAddr() string
	SendProto(*v1.Proto) error
	CloseWithProto(context.Context, *v1.Proto) error
	Close() error
}

// PreparedConnection can share one protobuf encoding across a fanout.
type PreparedConnection interface {
	Connection
	// SendPrepared must finish all access to prepared before returning. It may
	// retain the immutable bytes returned by Marshal, but not prepared or Message.
	SendPrepared(prepared *PreparedProto) error
}

// ConnectionHandler handles external connection lifecycle and decoded messages.
// Close may receive a canceled context when the connection or server is shutting down.
// Handlers that must finish cleanup should detach cancellation and apply a finite deadline.
type ConnectionHandler interface {
	Open(ctx context.Context, conn Connection) error
	Close(ctx context.Context, conn Connection)
	Handle(ctx context.Context, conn Connection, message *v1.Proto) (*v1.Proto, error)
}
