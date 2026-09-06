package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"flag"
	"log/slog"
	"net"
	"os"
	"time"

	"yola/event"
	"yola/examples/env"
	"yola/examples/message"
	"yola/gateway"
	"yola/locate"
	locateredis "yola/locate/redis"
	"yola/network/tcp"
	"yola/network/websocket"

	"github.com/go-kratos/kratos/v3"
	"github.com/go-kratos/kratos/v3/middleware/recovery"
)

var (
	name     = "gateway"
	id       string
	grpcPort string
	tcpPort  string
	wsPort   string
)

const stopTimeout = 10 * time.Second

func init() {
	flag.StringVar(&id, "id", "gateway-1", "Kratos instance ID")
	flag.StringVar(&grpcPort, "grpc-port", "9010", "gRPC listen port")
	flag.StringVar(&tcpPort, "tcp-port", "3101", "TCP listen port")
	flag.StringVar(&wsPort, "ws-port", "3102", "WebSocket listen port")
}

type authenticator struct{}

func (authenticator) Authenticate(_ context.Context, serviceName string, token []byte, _ string) (string, error) {
	if serviceName != "whot" && serviceName != "ludo" {
		return "", gateway.ErrInvalidCredentials
	}
	prefix := []byte(message.AuthTokenPrefix)
	if len(token) <= len(prefix) || subtle.ConstantTimeCompare(token[:len(prefix)], prefix) != 1 {
		return "", gateway.ErrInvalidCredentials
	}
	uid := string(token[len(prefix):])
	if !locate.ValidUID(uid) {
		return "", gateway.ErrInvalidCredentials
	}
	return uid, nil
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
	tcpAddr := net.JoinHostPort("127.0.0.1", tcpPort)
	wsAddr := net.JoinHostPort("127.0.0.1", wsPort)
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

	gate, err := gateway.NewServer(
		gateway.Address(grpcAddr),
		gateway.AdvertiseHost(env.Host),
		gateway.Auth(authenticator{}),
		gateway.Locator(locateredis.New(redisClient)),
		gateway.Discovery(registry),
		gateway.Transport(
			tcp.NewServer(
				tcp.Address(tcpAddr),
				tcp.Middleware(recovery.Recovery()),
			),
			websocket.NewServer(
				websocket.Address(wsAddr),
				websocket.Middleware(recovery.Recovery()),
			),
		),
	)
	if err != nil {
		slog.Error("create Gateway", "error", err)
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
		if stopErr := gate.Stop(cleanupCtx); stopErr != nil {
			slog.Error("stop Gateway", "error", stopErr)
		}
	}()
	if _, err = eventBus.Subscribe(
		context.Background(),
		message.AnnouncementTopic,
		func(ctx context.Context, received event.Event) {
			if broadcastErr := gate.Broadcast(message.AnnouncementCommand, received.Payload); broadcastErr != nil &&
				!errors.Is(broadcastErr, gateway.ErrBroadcastQueueFull) {
				slog.WarnContext(ctx, "broadcast announcement", "error", broadcastErr)
			}
		},
	); err != nil {
		slog.Error("subscribe announcements", "error", err)
		return
	}
	app := kratos.New(
		kratos.Context(appCtx),
		kratos.ID(id),
		kratos.Name(name),
		kratos.Logger(logger),
		kratos.StopTimeout(stopTimeout),
		kratos.BeforeStart(gate.BeforeStart),
		kratos.Server(gate),
		kratos.Registrar(registry),
	)
	if err := app.Run(); err != nil {
		slog.Error("run Gateway", "error", err)
	}
}
