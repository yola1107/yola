package server

import (
	"errors"

	locateredis "yola/locate/redis"
	"yola/node"
	"yola/test/whot/internal/conf"
	"yola/test/whot/internal/service"

	"github.com/go-kratos/kratos/v3/registry"
	"github.com/redis/go-redis/v9"
)

func NewNodeServer(
	redisClient redis.UniversalClient,
	registrar registry.Registrar,
	game *service.Service,
	serverConfig *conf.Server,
	dataConfig *conf.Data,
) (*node.Server, error) {
	if game == nil {
		return nil, errors.New("whot service is required")
	}
	config := serverConfig.GetGrpc()
	timeout := config.GetTimeout().AsDuration()
	if timeout <= 0 {
		return nil, errors.New("node timeout must be positive")
	}
	locator := locateredis.New(redisClient)
	options := []node.Option{
		node.Registration(registrar),
		node.Address(config.GetAddr()),
		node.AdvertiseHost(dataConfig.GetAdvertiseHost()),
		node.HandlerTimeout(timeout),
		node.Stateful(locator),
		node.GateDirectory(locator),
		node.Drain(game.Drain),
	}
	if network := config.GetNetwork(); network != "" {
		options = append(options, node.Network(network))
	}
	server, err := node.NewServer(options...)
	if err != nil {
		return nil, err
	}
	game.RegisterNode(server)
	return server, nil
}
