package gateway

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"yola/api/protocol/v1"
	"yola/locate"
	"yola/network"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Server) Open(_ context.Context, conn network.Connection) error {
	if !s.admission.accepting.Load() {
		return errors.New("gateway is unavailable")
	}
	if !s.sessions.add(conn, s.authTimeout) {
		return errors.New("duplicate connection")
	}
	// Lost the race with shutdown: drop the session rather than accept after admission closed.
	if !s.admission.accepting.Load() {
		if sess := s.sessions.remove(conn); sess != nil {
			sess.detachForClose()
		}
		return errors.New("gateway is unavailable")
	}
	return nil
}

func (s *Server) Close(ctx context.Context, conn network.Connection) {
	sess := s.sessions.remove(conn)
	if sess == nil {
		return
	}
	binding := sess.detachForClose()
	if !locate.ValidGateBinding(binding) {
		return
	}
	cleanupCtx, cancel := s.cleanupContext(ctx)
	defer cancel()
	slog.Debug("session disconnected",
		"uid", binding.UID,
		"conn_id", binding.ConnID,
		"service", binding.ServiceName,
		"gate_id", binding.GateID,
	)
	s.unbindGate(cleanupCtx, binding)
	s.notifyDisconnect(cleanupCtx, binding)
}

func (s *Server) Handle(ctx context.Context, conn network.Connection, msg *v1.Proto) (*v1.Proto, error) {
	if msg.Op == v1.OpHeartbeat {
		if err := s.Heartbeat(ctx, conn); err != nil {
			return nil, err
		}
		msg.Op, msg.Body = v1.OpHeartbeatReply, nil
		return msg, nil
	}
	sess := s.sessions.get(conn.ConnID())
	if sess == nil {
		return nil, errors.New("connection is not registered")
	}
	sess.handlerMu.Lock()
	defer sess.handlerMu.Unlock()
	switch msg.Op {
	case v1.OpAuth:
		if !sess.beginAuthentication() {
			_ = conn.Close()
			return nil, errors.New("authentication already attempted")
		}
		authCtx, cancel := context.WithDeadline(ctx, sess.authDeadline)
		s.authenticate(authCtx, sess, msg)
		cancel()
	case v1.OpRequest:
		s.forward(ctx, sess, msg)
	default:
		_ = conn.Close()
		return nil, errors.New("unsupported client operation")
	}
	return msg, nil
}

// Heartbeat 独立于业务 FIFO 续租；关闭会等待当前续租结束。
func (s *Server) Heartbeat(ctx context.Context, conn network.Connection) error {
	sess := s.sessions.get(conn.ConnID())
	if sess == nil {
		return errors.New("connection is not registered")
	}
	sess.heartbeatMu.Lock()
	defer sess.heartbeatMu.Unlock()
	return s.heartbeat(ctx, sess)
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
	renewCtx, cancel := context.WithTimeout(ctx, s.leaseTimeout)
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

func (s *Server) unbindGate(ctx context.Context, binding locate.GateBinding) {
	err := s.locator.UnbindGate(ctx, binding)
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
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
