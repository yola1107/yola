package gateway

import (
	"context"
	"errors"
	"log/slog"

	"yola/api/protocol/v1"
	"yola/internal/gateclient"
	"yola/locate"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// kickPrevious 位于已接纳的认证内，断线后的接管清理由 CleanupTimeout 限时。
func (s *Server) kickPrevious(ctx context.Context, previous locate.GateBinding) {
	if previous.GateID == s.identity.id {
		_ = s.kick(ctx, previous, v1.KickCodeSessionReplaced)
		return
	}
	ctx, cancel := s.cleanupContext(ctx)
	defer cancel()
	err := s.gateways.Kick(ctx, previous, v1.KickCodeSessionReplaced)
	if err == nil {
		return
	}
	code := status.Code(err)
	if errors.Is(err, gateclient.ErrUnavailable) {
		code = codes.Unavailable
	}
	if code == codes.Canceled {
		return
	}
	slog.ErrorContext(ctx, "kick previous connection failed",
		"uid", previous.UID,
		"conn_id", previous.ConnID,
		"service", previous.ServiceName,
		"gate_id", s.identity.id,
		"target_gate_id", previous.GateID,
		"code", int32(code),
		"status", code.String(),
		"error", err,
	)
}
