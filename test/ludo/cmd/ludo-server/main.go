package main

import (
	"flag"
	"log/slog"
	"time"

	"yola/node"
	"yola/test/internal/registry/etcd"
	"yola/test/internal/zapslog"
	"yola/test/ludo/internal/conf"

	"github.com/go-kratos/kratos/v3"
	"github.com/go-kratos/kratos/v3/config"
	"github.com/go-kratos/kratos/v3/config/file"
	_ "go.uber.org/automaxprocs"
)

var (
	Name     = conf.Name
	Version  = conf.Version
	flagconf string
	id       string
)

func init() {
	flag.StringVar(&flagconf, "conf", "ludo/configs", "config path, eg: -conf config.yaml")
	flag.StringVar(&id, "id", Name, "service instance ID")
}

func newApp(instanceID string, logger *slog.Logger, server *node.Server, registry *etcd.Registry) *kratos.App {
	return kratos.New(
		kratos.ID(instanceID),
		kratos.Name(Name),
		kratos.Version(Version),
		kratos.Metadata(server.Metadata()),
		kratos.Logger(logger),
		kratos.StopTimeout(10*time.Second),
		kratos.BeforeStart(server.BeforeStart),
		kratos.Server(server),
		kratos.Registrar(registry),
	)
}

func main() {
	flag.Parse()

	c := config.New(config.WithSource(file.NewSource(flagconf)))
	defer c.Close()
	if err := c.Load(); err != nil {
		panic(err)
	}

	var bootstrap conf.Bootstrap
	if err := c.Scan(&bootstrap); err != nil {
		panic(err)
	}
	if err := bootstrap.ValidateConfig(); err != nil {
		panic(err)
	}
	logger, cleanupLogger, err := zapslog.NewLogger(bootstrap.Log)
	if err != nil {
		panic(err)
	}
	defer cleanupLogger()

	app, cleanup, err := wireApp(id, bootstrap.Server, bootstrap.Data, bootstrap.Room, logger)
	if err != nil {
		panic(err)
	}
	defer cleanup()
	if err := app.Run(); err != nil {
		panic(err)
	}
}
