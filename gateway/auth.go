package gateway

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"yola/api/protocol/v1"
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

func (s *Server) heartbeat(ctx context.Context, sess *session) error {
	now := time.Now()
	binding, valid, due := sess.heartbeatRoute(now, s.leaseTTL/2)
	if !valid {
		_ = sess.conn.Close()
		return status.Error(codes.Aborted, "session lease expired")
	}
	if !due {
		return nil
	}
	renewCtx, cancel := context.WithTimeout(ctx, s.rpcTimeout)
	defer cancel()
	lease, err := s.locator.RenewGateLease(renewCtx, binding, s.leaseTTL)
	if err == nil {
		sess.finishHeartbeat(binding, now, lease.TTL)
		return nil
	}
	code := locateStatusCode(err)
	if code == codes.Aborted || code == codes.Internal {
		_ = sess.conn.Close()
		msg := "session binding changed"
		if code == codes.Internal {
			msg = "invalid Gate location state"
		}
		return status.Error(code, msg)
	}
	if code == codes.Canceled {
		return status.Error(codes.Canceled, context.Canceled.Error())
	}
	// DeadlineExceeded / Unavailable: keep the connection; the next heartbeat retries.
	return nil
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

func (s *Server) unbindGate(ctx context.Context, binding locate.GateBinding) {
	if err := s.locator.UnbindGate(ctx, binding); err != nil && !errors.Is(err, context.Canceled) {
		code := locateStatusCode(err)
		slog.ErrorContext(ctx, "unbind Gate failed",
			"uid", binding.UID,
			"conn_id", binding.ConnID,
			"service", binding.ServiceName,
			"gate_id", binding.GateID,
			"code", int32(code),
			"status", code.String(),
			"error", err,
		)
	}
}

func locateStatusCode(err error) codes.Code {
	switch {
	case errors.Is(err, locate.ErrGateNotFound), errors.Is(err, locate.ErrGateConflict):
		return codes.Aborted
	case errors.Is(err, context.Canceled):
		return codes.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return codes.DeadlineExceeded
	case errors.Is(err, locate.ErrInvalidGateBinding), errors.Is(err, locate.ErrInvalidGateLease),
		errors.Is(err, locate.ErrInvalidGateTTL):
		return codes.Internal
	default:
		return codes.Unavailable
	}
}
