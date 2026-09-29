package v1

const (
	MaxProtoSize            = 1 << 12  // 外部 transport 接受的序列化 Proto 大小上限
	KickCodeSessionReplaced = int32(1) // 会话已由另一条连接接管
	KickCodeServerShutdown  = int32(2) // Gateway 正在停止

	OpUnspecified    = int32(0) // 未指定操作
	OpAuth           = int32(1) // 认证新建的 Gateway 连接
	OpAuthReply      = int32(2) // 返回 Gateway 认证结果
	OpHeartbeat      = int32(3) // 心跳请求
	OpHeartbeatReply = int32(4) // 心跳响应
	OpRequest        = int32(5) // 客户端请求
	OpResponse       = int32(6) // 客户端响应
	OpPush           = int32(7) // 服务端推送
	OpKick           = int32(8) // 服务端主动断开连接
)
