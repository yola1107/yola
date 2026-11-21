package broadcastprobe

import (
	"encoding/binary"
	"math"
	"time"
)

const (
	Topic              = "yola.gateway.press.broadcast.v1"
	Command      int32 = 10005
	PayloadBytes       = 256
)

func Encode(sentAt time.Time) []byte {
	payload := make([]byte, PayloadBytes)
	binary.LittleEndian.PutUint64(payload, uint64(sentAt.UnixNano()))
	return payload
}

func Decode(payload []byte) (time.Time, bool) {
	if len(payload) != PayloadBytes {
		return time.Time{}, false
	}
	nanoseconds := binary.LittleEndian.Uint64(payload)
	if nanoseconds == 0 || nanoseconds > math.MaxInt64 {
		return time.Time{}, false
	}
	return time.Unix(0, int64(nanoseconds)), true
}
