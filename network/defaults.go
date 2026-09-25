package network

import "time"

// DefaultSendQueueSize is the default outbound frame queue capacity for
// TCP and WebSocket servers. Client outbound queues may use a larger default.
const DefaultSendQueueSize = 32

// DefaultRequestQueueSize 限制认证后等待执行的业务帧数，不包含当前执行的请求。
const DefaultRequestQueueSize = 8

// DefaultMaxConnLimit is the maximum number of active connections.
const DefaultMaxConnLimit = 10000

// DefaultMaxConnPerIP is the default per-peer-IP connection cap.
const DefaultMaxConnPerIP = 100

// DefaultHandshakeTimeout bounds connection handshake and post-handshake Open.
const DefaultHandshakeTimeout = 15 * time.Second

// DefaultHandlerTimeout bounds one inbound handler call.
const DefaultHandlerTimeout = 3 * time.Second

// DefaultWriteTimeout is the per-frame write timeout.
const DefaultWriteTimeout = 10 * time.Second
