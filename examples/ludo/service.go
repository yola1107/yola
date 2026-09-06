package main

import (
	"context"

	"google.golang.org/protobuf/types/known/wrapperspb"
)

type echoService struct{}

func (echoService) Echo(_ context.Context, in *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
	return wrapperspb.String("ludo: " + in.Value), nil
}
