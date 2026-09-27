package redis

import (
	"context"
	"errors"
	"net"
	"reflect"
	"time"

	"yola/locate"

	"github.com/redis/go-redis/v9"
)

var _ locate.Locator = (*locator)(nil)

type locator struct {
	client redis.UniversalClient
}

// New 使用调用方持有的 Redis client；调用方须启用 ContextTimeoutEnabled 并设置有限 I/O timeout。
// Locator 不修改共享 client；取消不能撤销已开始的 I/O 或已完成的写入。
func New(client redis.UniversalClient) locate.Locator {
	if isNilClient(client) {
		return nil
	}
	return &locator{client: client}
}

func isNilClient(client redis.UniversalClient) bool {
	if client == nil {
		return true
	}
	value := reflect.ValueOf(client)
	return value.Kind() == reflect.Pointer && value.IsNil()
}

func (l *locator) Ping(ctx context.Context) error {
	return locatorError(ctx, l.client.Ping(ctx).Err())
}

func (l *locator) run(ctx context.Context, script *redis.Script, key string, args ...any) ([]any, error) {
	values, err := script.Run(ctx, l.client, []string{key}, args...).Slice()
	return values, locatorError(ctx, err)
}

// locatorError 仅为网络超时补充已生效的 caller 取消原因，保留原始错误及成功结果。
func locatorError(ctx context.Context, err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var timeout net.Error
	if !errors.As(err, &timeout) || !timeout.Timeout() {
		return err
	}
	if cause := ctx.Err(); cause != nil {
		return errors.Join(err, cause)
	}
	// socket deadline 可能先于 context 的取消通知生效。
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return errors.Join(err, context.DeadlineExceeded)
	}
	return err
}
