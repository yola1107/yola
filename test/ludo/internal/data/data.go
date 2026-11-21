package data

import (
	"log/slog"

	"yola/registry/etcd"
	"yola/test/internal/xredis"
	"yola/test/ludo/internal/biz"
	"yola/test/ludo/internal/conf"

	"github.com/google/wire"
	"github.com/redis/go-redis/v9"
)

// ProviderSet 装配 Redis 客户端、Node 注册中心和玩家仓储。
var ProviderSet = wire.NewSet(NewRedisClient, NewRegistry, NewPlayerRepo)

type playerRepo struct {
	redis redis.UniversalClient
}

func NewPlayerRepo(client redis.UniversalClient) biz.PlayerRepo {
	return &playerRepo{redis: client}
}

func NewRedisClient(config *conf.Data) (redis.UniversalClient, func(), error) {
	client, err := xredis.NewClient(
		xredis.WithAddress(config.Redis.Addr),
		xredis.WithPassword(config.Redis.Password),
		xredis.WithDB(int(config.Redis.Db)),
	)
	if err != nil {
		return nil, nil, err
	}
	return client, func() {
		if err := client.Close(); err != nil {
			slog.Error("close Redis client", "error", err)
		}
	}, nil
}

func NewRegistry(config *conf.Data) (*etcd.Registry, func(), error) {
	registry, err := etcd.NewNodeRegistry(
		etcd.WithEndpoints(config.Registry.Endpoints...),
		etcd.WithPrefix(config.Registry.Prefix),
		etcd.WithDialTimeout(config.Registry.DialTimeout.AsDuration()),
	)
	if err != nil {
		return nil, nil, err
	}
	return registry, func() {
		if err := registry.Close(); err != nil {
			slog.Error("close service registry", "error", err)
		}
	}, nil
}
