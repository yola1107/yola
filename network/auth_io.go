package network

import (
	"context"
	"errors"
	"net"
)

// AuthenticationIOError 优先返回 ctx 错误，再将网络超时映射为 context.DeadlineExceeded。
func AuthenticationIOError(ctx context.Context, err error) error {
	if cause := ctx.Err(); cause != nil {
		return cause
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return context.DeadlineExceeded
	}
	return err
}
