package websocket

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"yola/api/protocol/v1"
	"yola/network"
	"yola/network/internal/inbound"

	"github.com/go-kratos/kratos/v3/transport"
	"github.com/gorilla/websocket"
	"google.golang.org/grpc/status"
)

func (s *Server) handleConnections(w http.ResponseWriter, r *http.Request) {
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
	var dispatcher *inbound.Dispatcher
	if _, ok := s.handler.(network.HeartbeatHandler); ok {
		dispatcher = inbound.New(connectionCtx, ch, invoke, s.config.requestQueueSize)
		defer dispatcher.Stop()
	}
	for {
		message := &v1.Proto{}
		if err := ch.readFrame(message); err != nil {
			warnUnexpectedNetworkError("[websocket] read failed", ch.ConnID(), err)
			return
		}
		if message.Op == v1.OpHeartbeat {
			message.Body = nil
		}
		if dispatcher != nil {
			if err := dispatcher.Handle(message); err != nil {
				warnUnexpectedNetworkError("[websocket] inbound dispatch failed", ch.ConnID(), err)
				return
			}
			continue
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

func (s *Server) reserveConnection(remoteIP string) bool {
	// lifecycle 锁保证最后一次 Add 先于 stopConnections 的 Wait。
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.stopped || s.connCount >= s.config.maxConnLimit ||
		s.connectionsPerIP[remoteIP] >= s.config.maxConnPerIP {
		return false
	}
	s.connCount++
	s.connectionsPerIP[remoteIP]++
	s.connWG.Add(1)
	return true
}

func (s *Server) commitConnection(ctx context.Context, conn *websocket.Conn) *Channel {
	// Stop 要么将此 channel 纳入快照，要么先关闭准入并拒绝提交。
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.stopped {
		return nil
	}
	ch := newChannel(ctx, conn, s.config.codec, *s.config.channel)
	s.channels[ch.ConnID()] = ch
	return ch
}

func (s *Server) releaseConnection(remoteIP string, ch *Channel) {
	s.lifecycleMu.Lock()
	if ch != nil {
		delete(s.channels, ch.ConnID())
	}
	if s.connectionsPerIP[remoteIP] == 1 {
		delete(s.connectionsPerIP, remoteIP)
	} else {
		s.connectionsPerIP[remoteIP]--
	}
	s.connCount--
	s.lifecycleMu.Unlock()
	s.connWG.Done()
}

func (s *Server) checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	if len(s.config.allowedOrigins) > 0 {
		_, ok := s.config.allowedOrigins[origin]
		return ok
	}
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || (u.Path != "" && u.Path != "/") {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

func (s *Server) rejectConnection(ctx context.Context, w http.ResponseWriter, remoteIP string) {
	w.WriteHeader(http.StatusServiceUnavailable)
	slog.WarnContext(ctx, "[websocket] connection rejected",
		"remote_ip", remoteIP,
		"connection_limit", s.config.maxConnLimit,
		"per_ip_limit", s.config.maxConnPerIP,
		"reason", "connection limit reached",
	)
}
