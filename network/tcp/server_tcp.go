package tcp

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"runtime/debug"
	"time"

	"yola/api/protocol/v1"
	"yola/network"

	"github.com/go-kratos/kratos/v3/transport"
	"github.com/google/uuid"
	"google.golang.org/grpc/status"
)

func (s *Server) acceptTCP(ctx context.Context, lis net.Listener) error {
	for {
		conn, err := lis.Accept()
		if err != nil {
			return err
		}
		connID := uuid.NewString()
		remoteAddr := conn.RemoteAddr().String()
		tcpConn := underlyingTCPConn(conn)
		if tcpConn == nil {
			_ = conn.Close()
			return fmt.Errorf("tcp: accepted connection is %T", conn)
		}
		if err := configureTCPConnection(tcpConn); err != nil {
			slog.Error("[tcp] configure connection failed",
				"conn_id", connID,
				"remote_addr", remoteAddr,
				"error", err,
			)
			_ = conn.Close()
			continue
		}
		if !s.trackConnection(conn) {
			slog.Warn("[tcp] connection rejected",
				"conn_id", connID,
				"remote_addr", remoteAddr,
				"reason", "connection limit reached",
			)
			_ = conn.Close()
			continue
		}
		go s.serveAcceptedTCP(ctx, conn, connID, remoteAddr)
	}
}

func (s *Server) serveAcceptedTCP(ctx context.Context, conn net.Conn, connID, remoteAddr string) {
	// Recovery runs before untracking, and untracking completes before shutdown observes Done.
	defer s.connWG.Done()
	defer s.untrackConnection(conn)
	defer func() {
		if value := recover(); value != nil {
			slog.Error("[tcp] connection panic",
				"conn_id", connID,
				"remote_addr", remoteAddr,
				"value", value,
				"stack", string(debug.Stack()),
			)
		}
	}()
	s.serveTCP(ctx, conn, connID)
}

func configureTCPConnection(conn *net.TCPConn) error {
	if err := conn.SetKeepAlive(false); err != nil {
		return fmt.Errorf("disable keepalive: %w", err)
	}
	if err := conn.SetReadBuffer(defaultSocketBufferSize); err != nil {
		return fmt.Errorf("set read buffer: %w", err)
	}
	if err := conn.SetWriteBuffer(defaultSocketBufferSize); err != nil {
		return fmt.Errorf("set write buffer: %w", err)
	}
	return nil
}

func (s *Server) trackConnection(conn net.Conn) bool {
	remoteIP, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
	// The lifecycle lock orders connection admission before shutdown; an admitted
	// connection remains counted while serveTCP may add its writer goroutine.
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.stopped || int32(len(s.conns)) >= s.config.maxConnLimit ||
		s.connectionsPerIP[remoteIP] >= s.config.maxConnPerIP {
		return false
	}
	s.conns[conn] = remoteIP
	s.connectionsPerIP[remoteIP]++
	s.connWG.Add(1)
	return true
}

func (s *Server) untrackConnection(conn net.Conn) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	remoteIP := s.conns[conn]
	delete(s.conns, conn)
	if s.connectionsPerIP[remoteIP] == 1 {
		delete(s.connectionsPerIP, remoteIP)
	} else {
		s.connectionsPerIP[remoteIP]--
	}
}

func underlyingTCPConn(conn net.Conn) *net.TCPConn {
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		return tcpConn
	}
	if tlsConn, ok := conn.(*tls.Conn); ok {
		tcpConn, _ := tlsConn.NetConn().(*net.TCPConn)
		return tcpConn
	}
	return nil
}

func (s *Server) serveTCP(baseCtx context.Context, conn net.Conn, connID string) {
	rAddr := conn.RemoteAddr().String()
	ctx, cancel := context.WithCancel(baseCtx)
	defer cancel()
	slog.Debug("[tcp] connection accepted",
		"conn_id", connID,
		"local_addr", conn.LocalAddr().String(),
		"remote_addr", rAddr,
	)
	ch := newChannel(defaultSendQueueSize, s.config.codec)
	rr := bufio.NewReaderSize(conn, defaultIOBufferSize)
	wr := bufio.NewWriterSize(conn, defaultIOBufferSize)
	defer ch.close()
	defer conn.Close()
	clientConn := tcpConnection{
		remoteAddr: rAddr,
		conn:       conn,
		ch:         ch,
	}
	ch.ip, _, _ = net.SplitHostPort(rAddr)
	ch.connID = connID
	ch.cancel = cancel
	connectionCtx := s.connectionContext(ctx, ch)
	handshakeDeadline := time.Now().Add(s.config.handshakeTimeout)
	openCtx, cancelOpen := context.WithDeadline(connectionCtx, handshakeDeadline)
	stopHandshake := context.AfterFunc(openCtx, func() {
		ch.close()
		_ = conn.Close()
		if errors.Is(openCtx.Err(), context.DeadlineExceeded) {
			slog.Warn("[tcp] handshake timeout",
				"conn_id", connID,
				"remote_addr", rAddr,
			)
		}
	})
	if err := s.handler.Open(openCtx, clientConn); err != nil {
		stopHandshake()
		cancelOpen()
		slog.Warn("[tcp] connection rejected",
			"conn_id", ch.connID,
			"remote_addr", rAddr,
			"error", err,
		)
		return
	}
	defer s.handler.Close(connectionCtx, clientConn)
	if !stopHandshake() {
		cancelOpen()
		return
	}
	cancelOpen()
	invoke := network.NewInvoker(s.handler, clientConn, s.config.timeout, s.config.middlewares...)
	s.connWG.Add(1)
	go func() {
		defer s.connWG.Done()
		s.dispatchTCP(conn, wr, ch)
	}()
	err := s.readTCPMessages(connectionCtx, clientConn, invoke, rr, handshakeDeadline)
	cancel()
	if err != nil && !errors.Is(err, context.Canceled) && !isConnectionClosedError(err) {
		slog.Warn("[tcp] read failed",
			"conn_id", ch.connID,
			"error", err,
		)
	}
	slog.Debug("[tcp] disconnected",
		"conn_id", ch.connID,
	)
}

func (s *Server) readTCPMessages(ctx context.Context, clientConn tcpConnection, invoke network.Invoker, reader *bufio.Reader, deadline time.Time) error {
	ch := clientConn.ch
	for {
		if err := clientConn.conn.SetReadDeadline(deadline); err != nil {
			return err
		}
		message := new(v1.Proto)
		if err := readFrame(reader, s.config.codec, message); err != nil {
			return err
		}
		if message.Op == v1.OpHeartbeat {
			deadline = time.Now().Add(s.config.heartbeatTimeout)
			message.Body = nil
		}
		reply, err := invoke(ctx, message)
		if err != nil {
			code := status.Code(err)
			slog.WarnContext(ctx, "[tcp] handler failed",
				"conn_id", ch.connID,
				"code", int32(code),
				"status", code.String(),
				"error", err,
			)
			return nil
		}
		if err := ch.reply(ctx, reply); err != nil {
			return err
		}
	}
}

func (s *Server) connectionContext(ctx context.Context, ch *channel) context.Context {
	endpoint := ""
	if s.endpoint != nil {
		endpoint = s.endpoint.String()
	}
	tr := network.NewTransport(network.KindTCP, endpoint, ch.ip, ch.connID)
	return transport.NewServerContext(ctx, tr)
}

type tcpConnection struct {
	remoteAddr string
	conn       net.Conn
	ch         *channel
}

func (c tcpConnection) ConnID() string              { return c.ch.connID }
func (c tcpConnection) RemoteAddr() string          { return c.remoteAddr }
func (c tcpConnection) SendProto(p *v1.Proto) error { return c.ch.push(p) }
func (c tcpConnection) CloseWithProto(ctx context.Context, p *v1.Proto) error {
	err := c.ch.closeWithProto(ctx, p)
	if !c.ch.isClosed() {
		return err
	}
	_ = c.conn.Close()
	return err
}

func (c tcpConnection) Close() error {
	c.ch.close()
	return c.conn.Close()
}

var _ network.Connection = tcpConnection{}

func (s *Server) dispatchTCP(conn net.Conn, wr *bufio.Writer, ch *channel) {
	var err error
	defer func() { ch.finishWriter(err) }()
	defer conn.Close()
	for {
		event, ready := ch.next()
		if !ready {
			break
		}
		err = s.writeTCPEvent(conn, wr, event.proto)
		if event.done != nil {
			event.done <- err
		}
		if err != nil || event.done != nil {
			break
		}
	}
	if err != nil && !isConnectionClosedError(err) {
		slog.Warn("[tcp] write failed",
			"conn_id", ch.connID,
			"error", err,
		)
	}
}

func (s *Server) writeTCPEvent(conn net.Conn, wr *bufio.Writer, message *v1.Proto) error {
	if err := conn.SetWriteDeadline(time.Now().Add(s.config.writeTimeout)); err != nil {
		return err
	}
	if err := writeFrame(wr, s.config.codec, message); err != nil {
		return err
	}
	return wr.Flush()
}
