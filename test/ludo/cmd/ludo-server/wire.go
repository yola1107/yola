//go:build wireinject
// +build wireinject

package main

import (
	"log/slog"

	"yola/test/ludo/internal/biz"
	"yola/test/ludo/internal/conf"
	"yola/test/ludo/internal/data"
	"yola/test/ludo/internal/server"
	"yola/test/ludo/internal/service"

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
