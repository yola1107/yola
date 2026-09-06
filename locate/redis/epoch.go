package redis

import (
	"context"
	"errors"
	"time"

	"yola/locate"

	"github.com/redis/go-redis/v9"
)

func (l *locator) RegisterNodeEpoch(ctx context.Context, serviceName, nodeID, epoch string, ttl time.Duration) error {
	ttlMillis, err := nodeEpochLeaseMilliseconds(serviceName, nodeID, epoch, ttl)
	if err != nil {
		return err
	}
	set, err := l.client.SetNX(ctx, nodeEpochKey(serviceName, nodeID), epoch, time.Duration(ttlMillis)*time.Millisecond).Result()
	if err != nil {
		return err
	}
	if !set {
		return locate.ErrNodeEpochConflict
	}
	return nil
}

func (l *locator) RenewNodeEpoch(ctx context.Context, serviceName, nodeID, epoch string, ttl time.Duration) error {
	ttlMillis, err := nodeEpochLeaseMilliseconds(serviceName, nodeID, epoch, ttl)
	if err != nil {
		return err
	}
	values, err := l.run(ctx, renewNodeEpochScript, nodeEpochKey(serviceName, nodeID), epoch, ttlMillis)
	if err != nil {
		return err
	}
	return decodeNodeEpochResult(values)
}

func (l *locator) LocateNodeEpoch(ctx context.Context, serviceName, nodeID string) (string, error) {
	if !locate.ValidServiceName(serviceName) || nodeID == "" {
		return "", locate.ErrInvalidNodeEpoch
	}
	epoch, err := l.client.Get(ctx, nodeEpochKey(serviceName, nodeID)).Result()
	if errors.Is(err, redis.Nil) {
		return "", locate.ErrNodeEpochNotFound
	}
	if err != nil {
		return "", err
	}
	if epoch == "" {
		return "", locate.ErrInvalidNodeEpoch
	}
	return epoch, nil
}

func (l *locator) UnregisterNodeEpoch(ctx context.Context, serviceName, nodeID, epoch string) error {
	if !locate.ValidServiceName(serviceName) || nodeID == "" || epoch == "" {
		return locate.ErrInvalidNodeEpoch
	}
	return l.deleteIfValueMatches(ctx, nodeEpochKey(serviceName, nodeID), epoch)
}

func nodeEpochLeaseMilliseconds(serviceName, nodeID, epoch string, ttl time.Duration) (int64, error) {
	if !locate.ValidServiceName(serviceName) || nodeID == "" || epoch == "" || ttl < time.Millisecond {
		return 0, locate.ErrInvalidNodeEpoch
	}
	return ttl.Milliseconds(), nil
}
