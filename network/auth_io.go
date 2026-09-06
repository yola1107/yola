package network

import (
	"context"
	"errors"
	"net"
)

// AuthenticationIOError maps dial/read failures during Auth onto ctx errors when
// the authentication deadline fired.
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
