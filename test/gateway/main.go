package main

import (
	"context"
	"errors"
	"log/slog"
	"runtime"
	"strconv"
	"time"

	"yola/event"
	"yola/event/nats"
	"yola/gateway"
	locateredis "yola/locate/redis"
	"yola/network/websocket"
	"yola/test/internal/broadcastprobe"
	"yola/test/internal/registry/etcd"
	"yola/test/internal/xredis"
	"yola/test/internal/zapslog"

	"github.com/go-kratos/kratos/v3"
	"github.com/go-kratos/kratos/v3/middleware/recovery"
)

const Name = "gateway"

const stopTimeout = 10 * time.Second

func main() {
	cfg, err := parseConfig()
	if err != nil {
		panic(err)
	}
	logger, cleanupLogger, err := zapslog.New(zapslog.WithAppName(Name), zapslog.WithLevel(cfg.logLevel))
	if err != nil {
		panic(err)
	}
	defer cleanupLogger()
	appCtx, cancelApp := context.WithCancel(context.Background())
	defer cancelApp()

	registry, err := etcd.New(
		etcd.WithEndpoints(cfg.etcdEndpoints),
		etcd.WithPrefix(cfg.etcdPrefix),
	)
	if err != nil {
		panic(err)
	}
	defer registry.Close()

	redisClient, err := xredis.NewClient(
		xredis.WithAddress(cfg.redisAddress),
		xredis.WithPassword(cfg.redisPassword),
		xredis.WithDB(cfg.redisDB),
	)
	if err != nil {
		panic(err)
	}
	defer redisClient.Close()

	gate, err := gateway.NewServer(
		gateway.Address(cfg.grpcListen),
		gateway.AdvertiseHost(cfg.advertiseHost),
		gateway.Auth(authenticator{}),
		gateway.Locator(locateredis.New(redisClient)),
		gateway.Discovery(registry),
		gateway.RPCTimeout(cfg.rpcTimeout),
		gateway.Transport(
			websocket.NewServer(
				websocket.Address(cfg.wsListen),
				websocket.MaxConnLimit(int32(cfg.wsMaxConns)),
				websocket.MaxConnPerIP(int32(cfg.wsMaxConnsPerIP)),
				websocket.Middleware(recovery.Recovery()),
			),
		),
	)
	if err != nil {
		panic(err)
	}
	eventBus, err := nats.New(nats.WithURL(cfg.natsURL))
	if err != nil {
		panic(err)
	}
	defer func() {
		if closeErr := eventBus.Close(); closeErr != nil {
			logger.Error("close event bus", "error", closeErr)
		}
	}()
	defer func() {
		cancelApp()
		cleanupCtx, cancel := context.WithTimeout(context.Background(), stopTimeout)
		defer cancel()
		if stopErr := gate.Stop(cleanupCtx); stopErr != nil {
			logger.Error("stop Gateway", "error", stopErr)
		}
	}()
	if _, err = eventBus.Subscribe(context.Background(), broadcastprobe.Topic, func(ctx context.Context, received event.Event) {
		if broadcastErr := gate.Broadcast(broadcastprobe.Command, received.Payload); broadcastErr != nil &&
			!errors.Is(broadcastErr, gateway.ErrBroadcastQueueFull) &&
			!errors.Is(broadcastErr, gateway.ErrBroadcastUnavailable) {
			logger.WarnContext(ctx, "broadcast capacity probe", "error", broadcastErr)
		}
	}); err != nil {
		panic(err)
	}

	app := kratos.New(
		kratos.Context(appCtx),
		kratos.ID(cfg.id),
		kratos.Name(Name),
		kratos.Logger(logger),
		kratos.StopTimeout(stopTimeout),
		kratos.BeforeStart(gate.BeforeStart),
		kratos.AfterStart(func(ctx context.Context) error {
			go runBroadcastProbe(ctx, logger, gate, eventBus)
			return nil
		}),
		kratos.BeforeStop(func(context.Context) error {
			return eventBus.Close()
		}),
		kratos.Server(gate),
		kratos.Registrar(registry),
	)
	if err = app.Run(); err != nil {
		panic(err)
	}
}

type authenticator struct{}

func (authenticator) Authenticate(_ context.Context, serviceName string, token []byte, _ string) (string, error) {
	if serviceName != "ludo" && serviceName != "whot" {
		return "", gateway.ErrInvalidCredentials
	}
	raw := string(token)
	uid, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || uid <= 0 || strconv.FormatInt(uid, 10) != raw {
		return "", gateway.ErrInvalidCredentials
	}
	return raw, nil
}

func runBroadcastProbe(ctx context.Context, logger *slog.Logger, gate *gateway.Server, eventBus event.Publisher) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var ticks uint64
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if err := eventBus.Publish(ctx, event.Event{
				Topic:   broadcastprobe.Topic,
				Payload: broadcastprobe.Encode(now),
			}); err != nil {
				if ctx.Err() != nil || errors.Is(err, event.ErrClosed) {
					return
				}
				logger.WarnContext(ctx, "publish broadcast probe", "error", err)
			}
			ticks++
			if ticks%15 != 0 {
				continue
			}
			stats := gate.BroadcastStats()
			var memory runtime.MemStats
			runtime.ReadMemStats(&memory)
			logger.InfoContext(ctx, "gateway capacity status",
				"broadcast", stats,
				"heap_bytes", memory.HeapAlloc,
				"heap_sys_bytes", memory.HeapSys,
				"gc_total", memory.NumGC,
				"goroutines", runtime.NumGoroutine(),
			)
		}
	}
}
