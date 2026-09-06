package main

import (
	"context"
	"flag"
	"log/slog"
	"net"
	"os"
	"time"

	"yola/examples/env"
	"yola/examples/message"
	"yola/node"

	"github.com/go-kratos/kratos/v3"
)

var (
	name     = "ludo"
	id       string
	grpcPort string
)

const stopTimeout = 10 * time.Second

func init() {
	flag.StringVar(&id, "id", "ludo-1", "Kratos instance ID")
	flag.StringVar(&grpcPort, "grpc-port", "9002", "gRPC listen port")
}

func main() {
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level:     slog.LevelDebug,
		AddSource: true,
	})).With("service", name, "instance_id", id)
	slog.SetDefault(logger)
	appCtx, cancelApp := context.WithCancel(context.Background())
	defer cancelApp()

	grpcAddr := net.JoinHostPort(env.Host, grpcPort)
	registry, err := env.NewRegistry()
	if err != nil {
		slog.Error("create registry", "error", err)
		return
	}
	defer registry.Close()

	nodeServer, err := node.NewServer(node.Address(grpcAddr))
	if err != nil {
		slog.Error("create Ludo Node", "error", err)
		return
	}
	defer func() {
		cancelApp()
		cleanupCtx, cancel := context.WithTimeout(context.Background(), stopTimeout)
		defer cancel()
		if stopErr := nodeServer.Stop(cleanupCtx); stopErr != nil {
			slog.Error("stop Ludo Node", "error", stopErr)
		}
	}()
	node.Register(nodeServer, message.EchoCommand, echoService{}.Echo)

	app := kratos.New(
		kratos.Context(appCtx),
		kratos.ID(id),
		kratos.Name(name),
		kratos.Logger(logger),
		kratos.StopTimeout(stopTimeout),
		kratos.BeforeStart(nodeServer.BeforeStart),
		kratos.Server(nodeServer),
		kratos.Registrar(registry),
	)
	if err := app.Run(); err != nil {
		slog.Error("run Ludo", "error", err)
	}
}
