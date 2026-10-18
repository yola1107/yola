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

const (
	_statusOK           int64 = 1
	_statusMissing      int64 = 0
	_statusConflict     int64 = -1
	_statusBadLease     int64 = -2
	_statusInvalid      int64 = -3
	_statusMalformed    int64 = -4
	_statusNodeMissing  int64 = -5
	_statusNodeConflict int64 = -6
)

var errInvalidScriptResult = errors.New("locate redis returned an invalid result")

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

func (l *locator) runScript(ctx context.Context, script *redis.Script, keys []string, args ...any) ([]any, error) {
	values, err := script.Run(ctx, l.client, keys, args...).Slice()
	return values, locatorError(ctx, err)
}

func scriptStatus(values []any) int64 {
	if len(values) == 0 {
		return _statusMalformed
	}
	status, ok := values[0].(int64)
	if !ok {
		return _statusMalformed
	}
	return status
}

// ackIdempotentUnbind 判断解绑或注销脚本的返回状态是否成功。
// Missing 和 Conflict 均视为成功，以保持重试幂等。
func ackIdempotentUnbind(status int64) bool {
	switch status {
	case _statusOK, _statusMissing, _statusConflict:
		return true
	default:
		return false
	}
}
