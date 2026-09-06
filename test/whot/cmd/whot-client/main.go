package main

import (
	"context"
	"flag"
	"log/slog"
	"os/signal"
	"syscall"

	"yola/test/internal/zapslog"
	"yola/test/whot/tools/press"
)

const Name = "whot-client"

var flagconf string

func init() {
	flag.StringVar(&flagconf, "conf", "whot/configs", "config path, eg: -conf config.yaml")
}

func main() {
	flag.Parse()

	logger, cleanupLogger, err := zapslog.New(zapslog.WithAppName(Name))
	if err != nil {
		panic(err)
	}
	slog.SetDefault(logger)
	defer cleanupLogger()

	c, bootstrap, err := press.LoadConfig(flagconf)
	if err != nil {
		panic(err)
	}
	defer c.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	runner := press.NewRunner(ctx, bootstrap.LoadTest)
	if err := runner.Start(); err != nil {
		panic(err)
	}
	defer runner.Stop()
	<-ctx.Done()
}
