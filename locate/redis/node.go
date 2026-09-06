package redis

import (
	"context"
	"errors"
	"time"

	"yola/locate"

	"github.com/redis/go-redis/v9"
)

const nodeBindingTTL = 6 * time.Hour

func (l *locator) BindNode(ctx context.Context, serviceName, uid, nodeID string) error {
	if !locate.ValidNodeLocation(serviceName, uid, nodeID) {
		return locate.ErrInvalidNodeBinding
	}
	return l.client.Set(ctx, nodeKey(serviceName, uid), nodeID, nodeBindingTTL).Err()
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

func (l *locator) UnbindNode(ctx context.Context, serviceName, uid, nodeID string) error {
	if !locate.ValidNodeLocation(serviceName, uid, nodeID) {
		return locate.ErrInvalidNodeBinding
	}
	return l.deleteIfValueMatches(ctx, nodeKey(serviceName, uid), nodeID)
}
