package redis

import (
	"context"
	"encoding/base64"
	"errors"
	"time"

	"yola/locate"

	"github.com/redis/go-redis/v9"
)

const (
	nodeBindingTTL     = 6 * time.Hour
	nodeKeyPrefix      = "locate:node:"
	nodeEpochKeyPrefix = "locate:node:epoch:"
)

func (l *locator) BindNode(ctx context.Context, serviceName, uid, nodeID, epoch string) error {
	if !locate.ValidNodeLocation(serviceName, uid, nodeID) {
		return locate.ErrInvalidNodeBinding
	}
	if epoch == "" {
		return locate.ErrInvalidNodeEpoch
	}
	values, err := bindNodeScript.Run(ctx, l.client,
		[]string{nodeEpochKey(serviceName, nodeID), nodeKey(serviceName, uid)},
		epoch, nodeID, nodeBindingTTL.Milliseconds(),
	).Slice()
	if err != nil {
		return locatorError(ctx, err)
	}
	return decodeNodeEpochResult(values)
}

func (l *locator) LocateNode(ctx context.Context, serviceName, uid string) (string, error) {
	if !locate.ValidServiceName(serviceName) || !locate.ValidUID(uid) {
		return "", locate.ErrInvalidNodeBinding
	}
	nodeID, err := l.client.Get(ctx, nodeKey(serviceName, uid)).Result()
	if errors.Is(err, redis.Nil) {
		return "", locate.ErrNodeNotFound
	}
	if err != nil {
		return "", locatorError(ctx, err)
	}
	if !locate.ValidNodeLocation(serviceName, uid, nodeID) {
		return "", locate.ErrInvalidNodeBinding
	}
	return nodeID, nil
}

func (l *locator) RenewNode(ctx context.Context, serviceName, uid, nodeID, epoch string) error {
	if !locate.ValidNodeLocation(serviceName, uid, nodeID) {
		return locate.ErrInvalidNodeBinding
	}
	if epoch == "" {
		return locate.ErrInvalidNodeEpoch
	}
	values, err := renewNodeScript.Run(ctx, l.client,
		[]string{nodeEpochKey(serviceName, nodeID), nodeKey(serviceName, uid)},
		epoch, nodeID, nodeBindingTTL.Milliseconds(),
	).Slice()
	if err != nil {
		return locatorError(ctx, err)
	}
	switch scriptStatus(values) {
	case statusNodeMissing:
		return locate.ErrNodeNotFound
	case statusNodeConflict:
		return locate.ErrNodeConflict
	default:
		return decodeNodeEpochResult(values)
	}
}

func (l *locator) UnbindNode(ctx context.Context, serviceName, uid, nodeID, epoch string) error {
	if !locate.ValidNodeLocation(serviceName, uid, nodeID) {
		return locate.ErrInvalidNodeBinding
	}
	if epoch == "" {
		return locate.ErrInvalidNodeEpoch
	}
	values, err := unbindNodeScript.Run(ctx, l.client,
		[]string{nodeEpochKey(serviceName, nodeID), nodeKey(serviceName, uid)}, epoch, nodeID,
	).Slice()
	if err != nil {
		return locatorError(ctx, err)
	}
	return decodeNodeEpochResult(values)
}

func (l *locator) RegisterNodeEpoch(ctx context.Context, serviceName, nodeID, epoch string, ttl time.Duration) error {
	ttlMillis, err := nodeEpochLeaseMilliseconds(serviceName, nodeID, epoch, ttl)
	if err != nil {
		return err
	}
	set, err := l.client.SetNX(ctx, nodeEpochKey(serviceName, nodeID), epoch, time.Duration(ttlMillis)*time.Millisecond).Result()
	if err != nil {
		return locatorError(ctx, err)
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
		return "", locatorError(ctx, err)
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
	values, err := l.run(ctx, deleteIfValueMatchesScript, nodeEpochKey(serviceName, nodeID), epoch)
	if err != nil {
		return err
	}
	if ackIdempotentUnbind(scriptStatus(values)) {
		return nil
	}
	return errInvalidScriptResult
}

func nodeEpochLeaseMilliseconds(serviceName, nodeID, epoch string, ttl time.Duration) (int64, error) {
	if !locate.ValidServiceName(serviceName) || nodeID == "" || epoch == "" || ttl < time.Millisecond {
		return 0, locate.ErrInvalidNodeEpoch
	}
	return ttl.Milliseconds(), nil
}

func nodeKey(serviceName, uid string) string {
	return nodeLocationKey(nodeKeyPrefix, serviceName, uid)
}

func nodeEpochKey(serviceName, nodeID string) string {
	return nodeLocationKey(nodeEpochKeyPrefix, serviceName, nodeID)
}

func nodeLocationKey(prefix, serviceName, id string) string {
	service := base64.RawURLEncoding.EncodeToString([]byte(serviceName))
	key := base64.RawURLEncoding.EncodeToString([]byte(id))
	return prefix + "{" + service + "}:" + key
}

func decodeNodeEpochResult(values []any) error {
	switch scriptStatus(values) {
	case statusOK:
		return nil
	case statusMissing:
		return locate.ErrNodeEpochNotFound
	case statusConflict:
		return locate.ErrNodeEpochConflict
	default:
		return errInvalidScriptResult
	}
}
