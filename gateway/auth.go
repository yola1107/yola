package gateway

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"yola/api/protocol/v1"
	"yola/internal/gateclient"
	"yola/locate"

	"github.com/go-kratos/kratos/v3/transport"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func (s *Server) authenticate(ctx context.Context, sess *session, msg *v1.Proto) {
	body := msg.Body
	msg.Op, msg.Code, msg.Body = v1.OpAuthReply, int32(codes.Unauthenticated), nil
	connID := sess.conn.ConnID()
	if !s.admission.accepting.Load() {
		slog.DebugContext(ctx, "session authentication rejected",
			"conn_id", connID,
			"reason", "gateway unavailable",
		)
		return
	}
	var request v1.ClientAuthReq
	if err := proto.Unmarshal(body, &request); err != nil {
		slog.DebugContext(ctx, "session authentication rejected",
			"conn_id", connID,
			"reason", "invalid request",
			"error", err,
		)
		return
	}
	if !locate.ValidServiceName(request.ServiceName) {
		slog.DebugContext(ctx, "session authentication rejected",
			"conn_id", connID,
			"service", request.ServiceName,
			"reason", "invalid service",
		)
		return
	}
	uid, err := s.authenticateUID(ctx, request.ServiceName, request.Token, connID)
	if err != nil {
		code := status.Code(err)
		msg.Code = int32(code)
		switch code {
		case codes.Unauthenticated:
			slog.DebugContext(ctx, "session authentication rejected",
				"conn_id", connID,
				"service", request.ServiceName,
				"code", int32(code),
				"status", code.String(),
				"reason", "unauthenticated",
			)
		case codes.DeadlineExceeded:
			slog.WarnContext(ctx, "session authentication timed out",
				"conn_id", connID,
				"service", request.ServiceName,
			)
		}
		return
	}
	binding := locate.GateBinding{
		ServiceName:  request.ServiceName,
		UID:          uid,
		GateID:       s.identity.id,
		GateEndpoint: s.identity.endpoint,
		ConnID:       connID,
		BindingToken: uuid.NewString(),
	}
	if !s.admitAuthentication(ctx, sess) {
		msg.Code = int32(authenticationFenceCode(ctx, sess))
		return
	}
	defer s.admission.wg.Done()
	boundAt := time.Now()
	lease, previous, err := s.locator.BindGate(ctx, binding, s.leaseTTL)
	if err != nil {
		code := locateStatusCode(err)
		msg.Code = int32(code)
		if code != codes.Canceled {
			slog.ErrorContext(ctx, "bind Gate failed",
				"uid", uid,
				"conn_id", connID,
				"service", request.ServiceName,
				"gate_id", s.identity.id,
				"code", int32(code),
				"status", code.String(),
				"error", err,
			)
		}
		return
	}
	if !s.sessions.commitAuthentication(ctx, sess, lease, boundAt) {
		msg.Code = int32(authenticationFenceCode(ctx, sess))
		cleanupCtx, cancel := s.cleanupContext(ctx)
		s.unbindGate(cleanupCtx, lease.Binding)
		cancel()
		return
	}
	slog.DebugContext(ctx, "session connected",
		"uid", lease.Binding.UID,
		"conn_id", lease.Binding.ConnID,
		"service", lease.Binding.ServiceName,
		"gate_id", lease.Binding.GateID,
	)
	msg.Code = int32(codes.OK)
	if previous == nil || *previous == lease.Binding {
		return
	}
	s.kickPrevious(ctx, *previous)
}

func authenticationFenceCode(ctx context.Context, sess *session) codes.Code {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || !time.Now().Before(sess.authDeadline) {
		return codes.DeadlineExceeded
	}
	if ctx.Err() != nil {
		return codes.Canceled
	}
	return codes.Aborted
}

func (s *Server) authenticateUID(ctx context.Context, serviceName string, token []byte, connID string) (string, error) {
	remoteIP := ""
	if tr, ok := transport.FromServerContext(ctx); ok {
		remoteIP = tr.RequestHeader().Get("remote_ip")
	}
	uid, err := s.authenticator.Authenticate(ctx, serviceName, token, remoteIP)
	if err != nil {
		switch {
		case errors.Is(err, ErrInvalidCredentials):
			return "", status.Error(codes.Unauthenticated, "authentication failed")
		case errors.Is(err, context.Canceled):
			return "", status.Error(codes.Canceled, context.Canceled.Error())
		case errors.Is(err, context.DeadlineExceeded):
			return "", status.Error(codes.DeadlineExceeded, context.DeadlineExceeded.Error())
		default:
			slog.ErrorContext(ctx, "authentication backend unavailable",
				"conn_id", connID,
				"service", serviceName,
				"error", err,
			)
			return "", status.Error(codes.Unavailable, "authentication unavailable")
		}
	}
	if !locate.ValidUID(uid) {
		return "", status.Error(codes.Unauthenticated, "authentication failed")
	}
	return uid, nil
}

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
