package websocket

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"yola/api/protocol/v1"
	"yola/network"

	"github.com/go-kratos/kratos/v3/transport"
	"google.golang.org/grpc/status"
)

func (s *Server) handleConnections() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		remoteIP, _, _ := net.SplitHostPort(r.RemoteAddr)
		if !s.reserveConnection(remoteIP) {
			s.rejectConnection(r.Context(), w, remoteIP)
			return
		}
		conn, err := s.upgrader.Upgrade(w, r, nil)
		if err != nil {
			s.releaseConnection(remoteIP, nil)
			slog.WarnContext(r.Context(), "[websocket] upgrade failed",
				"remote_addr", r.RemoteAddr,
				"error", err,
			)
			return
		}
		ch := s.commitConnection(r.Context(), conn)
		if ch == nil {
			_ = conn.Close()
			s.releaseConnection(remoteIP, nil)
			return
		}
		s.serveWebsocket(ch, remoteIP)
	}
}

func (s *Server) serveWebsocket(ch *Channel, remoteIP string) {
	connectionCtx := s.connectionContext(ch.ctx, ch)
	slog.Debug("[websocket] connection accepted",
		"conn_id", ch.ConnID(),
		"local_addr", ch.conn.LocalAddr().String(),
		"remote_addr", ch.RemoteAddr(),
	)
	defer func() {
		_ = ch.closeWithReason("connection closed")
		<-ch.writerDone
		s.releaseConnection(remoteIP, ch)
		slog.Debug("[websocket] disconnected",
			"conn_id", ch.ConnID(),
		)
	}()

	handshakeDeadline := time.Now().Add(s.config.handshakeTimeout)
	openCtx, cancelOpen := context.WithDeadline(connectionCtx, handshakeDeadline)
	stopHandshake := context.AfterFunc(openCtx, func() {
		_ = ch.closeWithReason("handshake timeout")
		if errors.Is(openCtx.Err(), context.DeadlineExceeded) {
			slog.Warn("[websocket] handshake timeout",
				"conn_id", ch.ConnID(),
				"remote_addr", ch.RemoteAddr(),
			)
		}
	})
	if err := s.handler.Open(openCtx, ch); err != nil {
		stopHandshake()
		cancelOpen()
		slog.Warn("[websocket] connection rejected",
			"conn_id", ch.ConnID(),
			"error", err,
		)
		return
	}
	defer func() {
		ch.cancel()
		s.handler.Close(connectionCtx, ch)
	}()
	if !stopHandshake() {
		cancelOpen()
		return
	}
	cancelOpen()
	s.readWebSocketMessages(
		connectionCtx,
		ch,
		network.NewInvoker(s.handler, ch, s.config.timeout, s.config.middlewares...),
	)
}

func (s *Server) readWebSocketMessages(connectionCtx context.Context, ch *Channel, invoke network.Invoker) {
	for {
		message := new(v1.Proto)
		if err := ch.readFrame(message); err != nil {
			warnUnexpectedNetworkError("[websocket] read failed", ch.ConnID(), err)
			return
		}
		if message.Op == v1.OpHeartbeat {
			message.Body = nil
		}
		reply, err := invoke(connectionCtx, message)
		if err != nil {
			code := status.Code(err)
			slog.WarnContext(connectionCtx, "[websocket] handler failed",
				"conn_id", ch.ConnID(),
				"code", int32(code),
				"status", code.String(),
				"error", err,
			)
			return
		}
		if err := ch.SendProto(reply); err != nil {
			warnUnexpectedNetworkError("[websocket] response enqueue failed", ch.ConnID(), err)
			return
		}
	}
}

func (s *Server) connectionContext(ctx context.Context, ch *Channel) context.Context {
	remoteIP := ch.RemoteAddr()
	if host, _, err := net.SplitHostPort(remoteIP); err == nil {
		remoteIP = host
	}
	endpoint := ""
	if s.endpoint != nil {
		endpoint = s.endpoint.String()
	}
	tr := network.NewTransport(network.KindWebSocket, endpoint, remoteIP, ch.ConnID())
	return transport.NewServerContext(ctx, tr)
}
