//go:build wireinject
// +build wireinject

package main

import (
	"log/slog"

	"yola/test/whot/internal/biz"
	"yola/test/whot/internal/conf"
	"yola/test/whot/internal/data"
	"yola/test/whot/internal/server"
	"yola/test/whot/internal/service"

	"github.com/go-kratos/kratos/v3"
	"github.com/google/wire"
)

func wireApp(instanceID string, serverConfig *conf.Server, dataConfig *conf.Data, room *conf.Room, logger *slog.Logger) (*kratos.App, func(), error) {
	panic(wire.Build(
		server.ProviderSet,
		data.ProviderSet,
		biz.ProviderSet,
		service.ProviderSet,
		newApp,
	))
}
