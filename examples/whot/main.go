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
	locateredis "yola/locate/redis"
	"yola/node"

	"github.com/go-kratos/kratos/v3"
)

var (
	name     = "whot"
	id       string
	grpcPort string
)

const stopTimeout = 10 * time.Second

func init() {
	flag.StringVar(&id, "id", "whot-1", "stable Kratos instance ID")
	flag.StringVar(&grpcPort, "grpc-port", "9001", "gRPC listen port")
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

	redisClient, err := env.NewRedis()
	if err != nil {
		slog.Error("create Redis client", "error", err)
		return
	}
	defer redisClient.Close()

	nodeServer, err := node.NewServer(
		node.Address(grpcAddr),
		node.Locator(locateredis.New(redisClient)),
	)
	if err != nil {
		slog.Error("create Whot Node", "error", err)
		return
	}
	eventBus, err := env.NewEventBus()
	if err != nil {
		slog.Error("create event bus", "error", err)
		return
	}
	defer func() {
		if closeErr := eventBus.Close(); closeErr != nil {
			slog.Error("close event bus", "error", closeErr)
		}
	}()
	defer func() {
		cancelApp()
		cleanupCtx, cancel := context.WithTimeout(context.Background(), stopTimeout)
		defer cancel()
		if stopErr := nodeServer.Stop(cleanupCtx); stopErr != nil {
			slog.Error("stop Whot Node", "error", stopErr)
		}
	}()
	service := gameService{events: eventBus}
	node.Register(nodeServer, message.WhotEnterCommand, service.Enter)
	node.Register(nodeServer, message.WhotLeaveCommand, service.Leave)
	node.Register(nodeServer, message.EchoCommand, service.Echo)

	app := kratos.New(
		kratos.Context(appCtx),
		kratos.ID(id),
		kratos.Name(name),
		kratos.Logger(logger),
		kratos.Metadata(nodeServer.Metadata()),
		kratos.StopTimeout(stopTimeout),
		kratos.BeforeStart(nodeServer.BeforeStart),
		kratos.Server(nodeServer),
		kratos.Registrar(registry),
	)
	if err := app.Run(); err != nil {
		slog.Error("run Whot", "error", err)
	}
}
