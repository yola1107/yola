package network

import "time"

// DefaultSendQueueSize 是 TCP/WebSocket 服务端的默认发送队列容量；
// 客户端发送队列可使用更大的默认值。
const DefaultSendQueueSize = 32

// DefaultRequestQueueSize 限制认证后等待执行的业务帧数，不包含当前执行的请求。
const DefaultRequestQueueSize = 8

// DefaultMaxConnLimit 是活动连接数上限。
const DefaultMaxConnLimit = 10000

// DefaultMaxConnPerIP 是每个对端 IP 的默认连接数上限。
const DefaultMaxConnPerIP = 100

// DefaultHandshakeTimeout 限制握手及握手后 Open 的耗时。
const DefaultHandshakeTimeout = 15 * time.Second

// DefaultHandlerTimeout 限制一次入站 handler 调用的耗时。
const DefaultHandlerTimeout = 3 * time.Second

// DefaultWriteTimeout 是单帧写入超时。
const DefaultWriteTimeout = 10 * time.Second
