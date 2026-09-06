// Package auth 复用客户端认证回复规则，不持有 transport 或连接资源。
package auth

import (
	"errors"
	"fmt"

	"yola/api/protocol/v1"
)

// ReadReply 暂存认证回复前的 Push；read 负责解码和取消，rejected 保留 transport 的错误身份。
func ReadReply(transport string, rejected error, limit int, read func() (*v1.Proto, error)) ([]*v1.Proto, error) {
	var pending []*v1.Proto
	for {
		reply, err := read()
		if err != nil {
			return nil, err
		}
		if reply.Op == v1.OpPush {
			if len(pending) >= limit {
				return nil, errors.New(transport + ": too many pushes before authentication reply")
			}
			pending = append(pending, reply)
			continue
		}
		if reply.Op != v1.OpAuthReply {
			return nil, errors.New(transport + ": invalid authentication response")
		}
		if reply.Code != 0 {
			return nil, fmt.Errorf("%w: code=%d", rejected, reply.Code)
		}
		return pending, nil
	}
}
