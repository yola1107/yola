package redis

import (
	"context"
	"errors"
	"time"

	"yola/locate"

	"github.com/redis/go-redis/v9"
)

const nodeBindingTTL = 6 * time.Hour

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
		return err
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
		return "", err
	}
	if !locate.ValidNodeLocation(serviceName, uid, nodeID) {
		return "", locate.ErrInvalidNodeBinding
	}
	return nodeID, nil
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
		return err
	}
	return decodeNodeEpochResult(values)
}
