package v1

const (
	// MaxProtoSize is the maximum serialized Proto size accepted by external transports.
	MaxProtoSize = 1 << 12
	// KickCodeSessionReplaced indicates that a newer connection owns the session.
	KickCodeSessionReplaced = int32(1)
	// KickCodeServerShutdown indicates the Gateway is shutting down.
	KickCodeServerShutdown = int32(2)

	OpUnspecified    = int32(0) // unspecified operation
	OpAuth           = int32(1) // authenticate a newly established Gateway connection
	OpAuthReply      = int32(2) // report the Gateway authentication result
	OpHeartbeat      = int32(3) // heartbeat request
	OpHeartbeatReply = int32(4) // heartbeat response
	OpRequest        = int32(5) // client request
	OpResponse       = int32(6) // client response
	OpPush           = int32(7) // server push
	OpKick           = int32(8) // intentional server-side disconnect
)
