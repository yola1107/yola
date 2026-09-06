package node

import (
	"context"
	"errors"

	"yola/internal/gateclient"
	"yola/locate"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// PushToUID locates the player's current Gate binding and pushes a message.
func (s *Server) PushToUID(ctx context.Context, uid string, command int32, msg proto.Message) error {
	if !s.requests.admit() {
		return status.Error(codes.Unavailable, "node is stopping or stopped")
	}
	defer s.requests.done()
	if s.locator == nil {
		return status.Error(codes.FailedPrecondition, "gate locator is not configured")
	}
	serviceName := s.currentIdentity().serviceName
	if !locate.ValidServiceName(serviceName) {
		return status.Error(codes.FailedPrecondition, "node is not started")
	}
	if !locate.ValidUID(uid) {
		return status.Error(codes.InvalidArgument, "invalid push target")
	}
	if !validMessage(msg) {
		return status.Error(codes.InvalidArgument, "push message is required")
	}
	ctx, cancel := context.WithTimeout(normalizeContext(ctx), s.pushTimeout)
	defer cancel()
	lease, err := s.locator.LocateGate(ctx, serviceName, uid)
	if err != nil {
		return mapGateLocatorError(err)
	}
	if lease.TTL <= 0 || lease.Binding.ServiceName != serviceName || lease.Binding.UID != uid {
		return status.Error(codes.Internal, "invalid gateway route")
	}
	return s.pushToGate(ctx, lease.Binding, command, msg)
}

func (s *Server) pushToGate(ctx context.Context, binding locate.GateBinding, command int32, msg proto.Message) error {
	if !locate.ValidGateBinding(binding) {
		return status.Error(codes.Internal, "invalid gateway route")
	}
	return mapGatePushError(s.gateways.Push(ctx, binding, command, msg))
}

func mapGateLocatorError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.Is(err, locate.ErrGateNotFound):
		return status.Error(codes.NotFound, "gate binding not found")
	case errors.Is(err, locate.ErrInvalidGateBinding), errors.Is(err, locate.ErrInvalidGateLease):
		return status.Error(codes.Internal, "invalid gate locator state")
	default:
		return status.Error(codes.Unavailable, "gate locator is unavailable")
	}
}

func mapGatePushError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	}
	if _, ok := status.FromError(err); ok {
		return err
	}
	if errors.Is(err, gateclient.ErrInvalidEndpoint) || errors.Is(err, gateclient.ErrTransportSecurity) {
		return status.Error(codes.Internal, "invalid gateway route")
	}
	return status.Error(codes.Unavailable, "gateway is unavailable")
}
