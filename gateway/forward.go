package gateway

import (
	"context"
	"errors"
	"log/slog"
	"time"

	protocolv1 "yola/api/protocol/v1"
	"yola/locate"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Server) forward(ctx context.Context, sess *session, msg *protocolv1.Proto) {
	binding, valid := sess.route(time.Now())
	if !valid {
		_ = sess.conn.Close()
		msg.Op, msg.Code, msg.Body = protocolv1.OpResponse, int32(codes.Unauthenticated), nil
		return
	}
	rpcCtx, cancel := context.WithTimeout(ctx, s.rpcTimeout)
	defer cancel()
	route, err := s.backends.resolveForward(rpcCtx, binding)
	if err != nil {
		code := forwardStatusCode(err)
		switch code {
		case codes.Unavailable:
			logForwardError(ctx, slog.LevelError, "forward node unavailable", binding, code, err)
		case codes.Aborted, codes.Internal:
			logForwardError(ctx, slog.LevelWarn, "forward node fenced", binding, code, err)
		}
		msg.Op, msg.Code, msg.Body = protocolv1.OpResponse, int32(code), nil
		return
	}
	reply, err := route.forward(rpcCtx, msg.Cmd, msg.Body)
	if err != nil {
		code := status.Code(err)
		if code != codes.Canceled && code != codes.Aborted {
			logForwardError(ctx, slog.LevelError, "forward node failed", binding, code, err)
		}
		msg.Op, msg.Code, msg.Body = protocolv1.OpResponse, int32(code), nil
		return
	}
	msg.Op, msg.Code, msg.Body = protocolv1.OpResponse, int32(codes.OK), reply.GetBody()
	if !validExternalFrame(msg) {
		msg.Code, msg.Body = int32(codes.ResourceExhausted), nil
	}
}

func (s *Server) notifyDisconnect(ctx context.Context, binding locate.GateBinding) {
	route, err := s.backends.resolveNode(ctx, binding)
	if err != nil {
		slog.WarnContext(ctx, "disconnect notify unavailable",
			"uid", binding.UID,
			"conn_id", binding.ConnID,
			"service", binding.ServiceName,
			"gate_id", binding.GateID,
			"error", err,
		)
		return
	}
	err = route.disconnect(ctx)
	if err != nil {
		slog.WarnContext(ctx, "disconnect notify failed",
			"uid", binding.UID,
			"conn_id", binding.ConnID,
			"service", binding.ServiceName,
			"gate_id", binding.GateID,
			"node_id", route.nodeID,
			"error", err,
		)
	}
}

func forwardStatusCode(err error) codes.Code {
	switch {
	case errors.Is(err, context.Canceled):
		return codes.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return codes.DeadlineExceeded
	case errors.Is(err, locate.ErrInvalidNodeBinding), errors.Is(err, locate.ErrInvalidNodeEpoch):
		return codes.Internal
	case errors.Is(err, locate.ErrNodeEpochNotFound), errors.Is(err, locate.ErrNodeEpochConflict):
		return codes.Aborted
	default:
		return codes.Unavailable
	}
}

func logForwardError(
	ctx context.Context, level slog.Level, message string,
	binding locate.GateBinding, code codes.Code, err error,
) {
	slog.Log(ctx, level, message,
		"uid", binding.UID,
		"conn_id", binding.ConnID,
		"service", binding.ServiceName,
		"gate_id", binding.GateID,
		"code", int32(code),
		"status", code.String(),
		"error", err,
	)
}
