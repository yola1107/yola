package redis

import (
	"context"
	"reflect"

	"yola/locate"

	"github.com/redis/go-redis/v9"
)

var _ locate.Locator = (*locator)(nil)

type locator struct {
	client redis.UniversalClient
}

// New creates a locator with a caller-owned Redis client.
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
	return l.client.Ping(ctx).Err()
}

func (l *locator) run(ctx context.Context, script *redis.Script, key string, args ...any) ([]any, error) {
	return script.Run(ctx, l.client, []string{key}, args...).Slice()
}

func (l *locator) deleteIfValueMatches(ctx context.Context, key, expected string) error {
	values, err := l.run(ctx, deleteIfValueMatchesScript, key, expected)
	if err != nil {
		return err
	}
	if ackIdempotentUnbind(scriptStatus(values)) {
		return nil
	}
	return errInvalidScriptResult
}
