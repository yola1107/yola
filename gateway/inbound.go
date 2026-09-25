package gateway

import (
	"context"
	"errors"
	"log/slog"

	"yola/api/protocol/v1"
	"yola/locate"
	"yola/network"
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
