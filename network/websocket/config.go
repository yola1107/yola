package websocket

import (
	"errors"
	"time"

	"yola/network"
)

const (
	// DefaultMaxConnLimit is the maximum number of active channels.
	DefaultMaxConnLimit = network.DefaultMaxConnLimit
	// DefaultMaxConnPerIP is the default per-peer-IP connection cap.
	DefaultMaxConnPerIP = network.DefaultMaxConnPerIP
	// DefaultReadBufSize is the Gorilla read buffer size.
	DefaultReadBufSize = 4096
	// DefaultWriteBufSize is the Gorilla write buffer size.
	DefaultWriteBufSize = 4096
	// DefaultSendQueueSize is the outbound queue capacity (shared with TCP).
	DefaultSendQueueSize = network.DefaultSendQueueSize
	// DefaultHandshakeTimeout bounds HTTP upgrade and post-upgrade Open.
	DefaultHandshakeTimeout = network.DefaultHandshakeTimeout
	// DefaultMaxHeaderBytes is the maximum WebSocket handshake header size.
	DefaultMaxHeaderBytes = 16 << 10
	// DefaultWriteTimeout is the per-frame write timeout.
	DefaultWriteTimeout = network.DefaultWriteTimeout
	// DefaultPingInterval is the client heartbeat interval.
	DefaultPingInterval = 15 * time.Second
	// DefaultReadDeadline is the maximum interval between incoming frames.
	DefaultReadDeadline = 60 * time.Second
	// DefaultRequestTimeout bounds pending requests.
	DefaultRequestTimeout = 30 * time.Second
)

// ChannelConfig configures a WebSocket channel.
type ChannelConfig struct {
	WriteTimeout  time.Duration
	ReadDeadline  time.Duration
	SendQueueSize int
}

func defaultChannelConfig() *ChannelConfig {
	return &ChannelConfig{
		WriteTimeout:  DefaultWriteTimeout,
		ReadDeadline:  DefaultReadDeadline,
		SendQueueSize: DefaultSendQueueSize,
	}
}

func (c *ChannelConfig) validate() error {
	if c == nil {
		return errors.New("websocket: channel config is required")
	}
	if c.WriteTimeout <= 0 {
		return errors.New("websocket: channel write timeout must be positive")
	}
	if c.ReadDeadline <= 0 {
		return errors.New("websocket: channel read deadline must be positive")
	}
	if c.SendQueueSize <= 0 {
		return errors.New("websocket: channel send queue size must be positive")
	}
	return nil
}
