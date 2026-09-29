package network

import (
	"context"
	"errors"
	"net"
)

// AuthenticationIOError 在认证 deadline 到期时，
// 将认证期间的 dial/read 失败映射为 ctx 错误。
func AuthenticationIOError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return context.DeadlineExceeded
	}
	return err
}
