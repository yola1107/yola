package main

import (
	"context"
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

const stopTimeout = 10 * time.Second

func init() {
	flag.StringVar(&flagconf, "conf", "ludo/configs", "config path, eg: -conf config.yaml")
	flag.StringVar(&id, "id", Name, "service instance ID")
}

func newApp(instanceID string, logger *slog.Logger, server *node.Server, registry *etcd.Registry) (*kratos.App, func()) {
	appCtx, cancelApp := context.WithCancel(context.Background())
	app := kratos.New(
		kratos.Context(appCtx),
		kratos.ID(instanceID),
		kratos.Name(Name),
		kratos.Version(Version),
		kratos.Metadata(server.Metadata()),
		kratos.Logger(logger),
		kratos.StopTimeout(stopTimeout),
		kratos.BeforeStart(server.BeforeStart),
		kratos.Server(server),
		kratos.Registrar(registry),
	)
	return app, func() {
		// Run 的早退不会等待 Server；Wire 必须先停止 Node，再释放其外部依赖。
		cancelApp()
		cleanupCtx, cancel := context.WithTimeout(kratos.NewContext(context.Background(), app), stopTimeout)
		defer cancel()
		if stopErr := server.Stop(cleanupCtx); stopErr != nil {
			logger.Error("stop Ludo Node", "error", stopErr)
		}
	}
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
