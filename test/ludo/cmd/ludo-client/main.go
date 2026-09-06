package main

import (
	"context"
	"flag"
	"log/slog"
	"os/signal"
	"syscall"

	"yola/test/internal/zapslog"
	"yola/test/ludo/tools/press"
)

const Name = "ludo-client"

var (
	flagconf string
	logLevel string
)

func init() {
	flag.StringVar(&flagconf, "conf", "ludo/configs", "config path, eg: -conf config.yaml")
	flag.StringVar(&logLevel, "log-level", "info", "log level: debug, info, warn, or error")
}

func main() {
	flag.Parse()

	logger, cleanupLogger, err := zapslog.New(zapslog.WithAppName(Name), zapslog.WithLevel(logLevel))
	if err != nil {
		panic(err)
	}
	slog.SetDefault(logger)
	defer cleanupLogger()

	c, config, err := press.LoadConfig(flagconf)
	if err != nil {
		panic(err)
	}
	defer c.Close()

	runner := press.NewRunner(config)
	if err := runner.Start(); err != nil {
		panic(err)
	}
	defer runner.Stop()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
}
