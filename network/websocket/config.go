package websocket

import (
	"errors"
	"time"

	"yola/network"
)

// WebSocket 连接和消息处理的默认配置。
const (
	DefaultMaxConnLimit     = network.DefaultMaxConnLimit     // 活动连接数上限
	DefaultMaxConnPerIP     = network.DefaultMaxConnPerIP     // 每个对端 IP 的连接数上限
	DefaultReadBufSize      = 4096                            // Gorilla 读取缓冲区大小
	DefaultWriteBufSize     = 4096                            // Gorilla 写入缓冲区大小
	DefaultSendQueueSize    = network.DefaultSendQueueSize    // 发送队列容量，与 TCP 共用
	DefaultHandshakeTimeout = network.DefaultHandshakeTimeout // HTTP upgrade 及其后 Open 的超时
	DefaultMaxHeaderBytes   = 16 << 10                        // WebSocket 握手 header 大小上限
	DefaultWriteTimeout     = network.DefaultWriteTimeout     // 单帧写入超时
	DefaultPingInterval     = 15 * time.Second                // 客户端心跳间隔
	DefaultReadDeadline     = 60 * time.Second                // 两次入站帧之间的最长间隔
	DefaultRequestTimeout   = 30 * time.Second                // 请求结果等待超时
)

// ChannelConfig 配置 WebSocket channel。
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
