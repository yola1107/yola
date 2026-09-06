package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"time"

	"yola/examples/message"
	"yola/locate"
	"yola/network/tcp"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const requestInterval = 3 * time.Second

var (
	serviceName string
	gateAddr    string
	playerID    string
)

func init() {
	flag.StringVar(&serviceName, "service", "whot", "target game service: whot or ludo")
	flag.StringVar(&gateAddr, "addr", "127.0.0.1:3101", "Gateway TCP address")
	flag.StringVar(&playerID, "uid", fmt.Sprintf("player-%d", os.Getpid()), "authenticated player UID")
}

func main() {
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level:     slog.LevelDebug,
		AddSource: true,
	})).With("uid", playerID)
	slog.SetDefault(logger)
	if !locate.ValidUID(playerID) {
		slog.Error("invalid player UID", "uid", playerID)
		return
	}
	token := message.AuthTokenPrefix + playerID

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	for {
		runClient(ctx, gateAddr, serviceName, playerID, token)
		select {
		case <-ctx.Done():
			return
		case <-time.After(requestInterval):
		}
	}
}

func runClient(ctx context.Context, address, serviceName, uid, token string) {
	disconnected := make(chan struct{}, 1)
	client, err := tcp.NewClient(
		ctx,
		tcp.WithAddress(address),
		tcp.WithServiceName(serviceName),
		tcp.WithToken(token),
		tcp.WithDisconnectFunc(func() {
			select {
			case disconnected <- struct{}{}:
			default:
			}
		}),
		tcp.WithPushHandler(map[int32]tcp.PushHandler{
			message.WhotPushCommand:     handlePush,
			message.AnnouncementCommand: handlePush,
		}),
	)
	if err != nil {
		slog.Error("create TCP client", "error", err)
		return
	}
	defer client.Close()
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-disconnected:
			return
		case <-timer.C:
			if err := request(ctx, client, serviceName, uid); err != nil {
				slog.Error("request failed", "error", err)
				return
			}
			timer.Reset(requestInterval)
		}
	}
}

func request(ctx context.Context, client *tcp.Client, serviceName, uid string) error {
	requestCtx, requestCancel := context.WithTimeout(ctx, 5*time.Second)
	defer requestCancel()
	if serviceName == "whot" {
		if _, err := sendRequest(requestCtx, client, "enter", message.WhotEnterCommand, new(emptypb.Empty)); err != nil {
			return err
		}
	}

	body, err := sendRequest(requestCtx, client, "echo", message.EchoCommand, wrapperspb.String(uid+" hello"))
	if err != nil {
		return err
	}
	var reply wrapperspb.StringValue
	if err := proto.Unmarshal(body, &reply); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	slog.Info("response received", "body", reply.Value)

	if serviceName == "whot" {
		if _, err := sendRequest(requestCtx, client, "leave", message.WhotLeaveCommand, new(emptypb.Empty)); err != nil {
			return err
		}
		slog.Info("-----------")
	}
	return nil
}

func sendRequest(ctx context.Context, client *tcp.Client, operation string, command int32, msg proto.Message) ([]byte, error) {
	body, code, err := client.Request(ctx, command, msg)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", operation, err)
	}
	if code != 0 {
		return nil, fmt.Errorf("%s gateway code %d", operation, code)
	}
	return body, nil
}

func handlePush(body []byte) {
	var message wrapperspb.StringValue
	if err := proto.Unmarshal(body, &message); err != nil {
		slog.Error("decode push", "error", err)
		return
	}
	slog.Info("push received", "body", message.Value)
}
