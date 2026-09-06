package main

import (
	"context"
	"log/slog"

	"yola/event"
	"yola/examples/message"
	"yola/node"

	"github.com/go-kratos/kratos/v3/errors"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

type gameService struct {
	events event.Publisher
}

func (gameService) Enter(ctx context.Context, _ *emptypb.Empty) (*emptypb.Empty, error) {
	sess, err := sessionFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if err := sess.BindNode(ctx); err != nil {
		return nil, err
	}
	slog.DebugContext(ctx, "Enter && BindNode")
	return new(emptypb.Empty), nil
}

func (gameService) Leave(ctx context.Context, _ *emptypb.Empty) (*emptypb.Empty, error) {
	sess, err := sessionFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if err := sess.UnbindNode(ctx); err != nil {
		return nil, err
	}
	slog.DebugContext(ctx, "Leave && UnbindNode")
	return new(emptypb.Empty), nil
}

func (s gameService) Echo(ctx context.Context, in *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
	sess, err := sessionFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if pushErr := sess.Push(ctx, message.WhotPushCommand, wrapperspb.String(id+" push: "+in.Value)); pushErr != nil {
		return nil, pushErr
	}
	payload, err := proto.Marshal(wrapperspb.String(id + " announcement: " + in.Value))
	if err != nil {
		return nil, err
	}
	if err := s.events.Publish(ctx, event.Event{Topic: message.AnnouncementTopic, Payload: payload}); err != nil {
		slog.WarnContext(ctx, "publish announcement", "error", err)
	}
	slog.DebugContext(ctx, "Echo invoked")
	return wrapperspb.String(id + " " + "say hello"), nil
}

func sessionFromContext(ctx context.Context) (node.Session, error) {
	sess, ok := node.FromContext(ctx)
	if !ok {
		return nil, errors.Unauthorized("SESSION_NOT_FOUND", "authenticated session is missing")
	}
	return sess, nil
}
