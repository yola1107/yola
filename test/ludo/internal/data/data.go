package data

import (
	"log/slog"

	"yola/test/internal/registry/etcd"
	"yola/test/internal/xredis"
	"yola/test/ludo/internal/biz"
	"yola/test/ludo/internal/conf"

	"github.com/google/wire"
	"github.com/redis/go-redis/v9"
)

// ProviderSet wires Redis-backed Ludo data access.
var ProviderSet = wire.NewSet(NewRedisClient, NewRegistry, NewData, NewPlayerRepo)

type playerRepo struct {
	data *Data
}

func NewPlayerRepo(data *Data) biz.PlayerRepo {
	return &playerRepo{data: data}
}

type Data struct {
	redis redis.UniversalClient
}

func NewData(client redis.UniversalClient) *Data {
	return &Data{redis: client}
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
	registry, err := etcd.New(
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
