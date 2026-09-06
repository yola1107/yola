package websocket

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"yola/api/protocol/v1"
	"yola/network"
	"yola/network/internal/heartbeat"

	"github.com/go-kratos/kratos/v3/encoding"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

var (
	errNilPayload            = errors.New("websocket: nil payload")
	errInvalidHeartbeatState = errors.New("websocket: invalid heartbeat state")
)

const maxCloseReasonSize = 123

var readFrameBuffers = sync.Pool{
	New: func() any { return new([v1.MaxProtoSize]byte) },
}

// Channel is one Gorilla WebSocket connection.
type Channel struct {
	connID     string
	remoteAddr string
	conn       *websocket.Conn
	codec      encoding.Codec
	config     ChannelConfig
	ctx        context.Context
	cancel     context.CancelFunc
	outbound   chan outboundFrame
	// heartbeat is connection-scoped so stale replies cannot acknowledge a replacement Channel.
	heartbeat heartbeat.State
	closed    atomic.Bool
	sendMu    sync.Mutex
	connOnce  sync.Once
	connErr   error
	// writerErr is published by closing writerDone and read only after that close.
	writerErr  error
	writerDone chan struct{}
}

type outboundFrame struct {
	body      []byte
	heartbeat bool
	// done is non-nil only for the final frame, so it also marks writer termination.
	done chan error
}

func newChannel(ctx context.Context, conn *websocket.Conn, codec encoding.Codec, config ChannelConfig) *Channel {
	ctx, cancel := context.WithCancel(ctx)
	ch := &Channel{
		connID:     uuid.NewString(),
		remoteAddr: conn.RemoteAddr().String(),
		conn:       conn,
		codec:      codec,
		config:     config,
		ctx:        ctx,
		cancel:     cancel,
		outbound:   make(chan outboundFrame, config.SendQueueSize),
		writerDone: make(chan struct{}),
	}
	conn.SetReadLimit(v1.MaxProtoSize)
	context.AfterFunc(ch.ctx, ch.closeOnContext)
	go ch.writeLoop()
	return ch
}

// ConnID returns the physical connection identity.
func (ch *Channel) ConnID() string { return ch.connID }

// RemoteAddr returns the remote network address.
func (ch *Channel) RemoteAddr() string { return ch.remoteAddr }

// Closed reports whether the channel is closed.
func (ch *Channel) Closed() bool { return ch.closed.Load() }

// SendProto queues one Proto as a Binary frame.
func (ch *Channel) SendProto(p *v1.Proto) error {
	return ch.enqueueProto(p, false)
}

// SendPrepared queues a shared default-protobuf encoding for Gateway fanout.
// Custom codecs retain SendProto behavior because equal names do not prove equal wire bytes.
func (ch *Channel) SendPrepared(message *network.PreparedProto) error {
	if message == nil || message.Message() == nil {
		return errNilPayload
	}
	if !canOptimizeFrames(ch.codec) {
		return ch.SendProto(message.Message())
	}
	return ch.enqueueFrame(false, func() ([]byte, error) {
		body, err := message.Marshal()
		if err != nil {
			return nil, err
		}
		return body, validateFrameBody(body)
	})
}

// sendHeartbeat marks the frame so writeLoop, rather than the enqueue path,
// advances its reply-timeout state.
func (ch *Channel) sendHeartbeat() error {
	return ch.enqueueProto(&v1.Proto{Op: v1.OpHeartbeat}, true)
}

func (ch *Channel) enqueueProto(p *v1.Proto, heartbeat bool) error {
	if p == nil {
		return errNilPayload
	}
	return ch.enqueueFrame(heartbeat, func() ([]byte, error) {
		return marshalFrame(ch.codec, p)
	})
}

func (ch *Channel) enqueueFrame(heartbeat bool, encode func() ([]byte, error)) error {
	ch.sendMu.Lock()
	defer ch.sendMu.Unlock()
	if ch.closed.Load() {
		return network.ErrConnectionClosed
	}
	if len(ch.outbound) == cap(ch.outbound) {
		return network.ErrSendQueueFull
	}
	body, err := encode()
	if err != nil {
		return err
	}
	// sendMu serializes producers, so the capacity check above remains valid.
	ch.outbound <- outboundFrame{body: body, heartbeat: heartbeat}
	return nil
}

// CloseWithProto writes one final Binary frame before closing the connection.
func (ch *Channel) CloseWithProto(ctx context.Context, p *v1.Proto) error {
	body, err := marshalFrame(ch.codec, p)
	if err != nil {
		return err
	}
	ch.sendMu.Lock()
	if ch.closed.Load() {
		ch.sendMu.Unlock()
		return network.ErrConnectionClosed
	}
	ch.closed.Store(true)
	ch.sendMu.Unlock()
	frame := outboundFrame{body: body, done: make(chan error, 1)}
	select {
	case ch.outbound <- frame:
	case <-ch.writerDone:
		return ch.writerError()
	case <-ctx.Done():
		return errors.Join(ctx.Err(), ch.abort())
	}
	return ch.waitFinal(ctx, frame.done)
}

func (ch *Channel) waitFinal(ctx context.Context, result <-chan error) error {
	select {
	case err := <-result:
		return err
	default:
	}
	select {
	case err := <-result:
		return err
	case <-ch.writerDone:
		select {
		case err := <-result:
			return err
		default:
			return ch.writerError()
		}
	case <-ctx.Done():
		select {
		case err := <-result:
			return err
		default:
		}
		return errors.Join(ctx.Err(), ch.abort())
	}
}

func (ch *Channel) readFrame(p *v1.Proto) error {
	if err := ch.conn.SetReadDeadline(time.Now().Add(ch.config.ReadDeadline)); err != nil {
		return err
	}
	messageType, reader, err := ch.conn.NextReader()
	if err != nil {
		return err
	}
	if messageType != websocket.BinaryMessage {
		return errors.New("websocket: binary message required")
	}
	if !canOptimizeFrames(ch.codec) {
		body, readErr := io.ReadAll(reader)
		if readErr != nil {
			return readErr
		}
		return unmarshalFrame(ch.codec, body, p)
	}
	buffer := readFrameBuffers.Get().(*[v1.MaxProtoSize]byte)
	defer readFrameBuffers.Put(buffer)
	body, err := readBoundedFrame(reader, buffer[:])
	if err != nil {
		return err
	}
	return unmarshalFrame(ch.codec, body, p)
}

func readBoundedFrame(reader io.Reader, buffer []byte) ([]byte, error) {
	read := 0
	for {
		if read == len(buffer) {
			var extra [1]byte
			n, err := reader.Read(extra[:])
			switch {
			case n > 0:
				return nil, network.ErrFrameTooLarge
			case errors.Is(err, io.EOF):
				return buffer[:read], nil
			case err != nil:
				return nil, err
			default:
				continue
			}
		}
		n, err := reader.Read(buffer[read:])
		read += n
		switch {
		case errors.Is(err, io.EOF):
			return buffer[:read], nil
		case err != nil:
			return nil, err
		}
	}
}

func (ch *Channel) writeLoop() {
	var writeErr error
	defer func() { ch.finishWriter(writeErr) }()
	for {
		select {
		case <-ch.ctx.Done():
			writeErr = ch.ctx.Err()
			return
		case frame := <-ch.outbound:
			writeErr = ch.writeOutbound(frame)
			if frame.done != nil {
				writeErr = errors.Join(writeErr, ch.closeWithReason("kicked"))
				frame.done <- writeErr
				return
			}
			if writeErr == nil {
				continue
			}
			warnUnexpectedNetworkError("[websocket] write failed", ch.connID, writeErr)
			writeErr = errors.Join(writeErr, ch.closeWithReason("write failed"))
			return
		}
	}
}

func (ch *Channel) writeOutbound(frame outboundFrame) error {
	if frame.heartbeat && !ch.heartbeat.BeginWrite() {
		return errInvalidHeartbeatState
	}
	if err := ch.writeMessage(frame.body); err != nil {
		return err
	}
	if !frame.heartbeat || ch.heartbeat.FinishWrite() {
		return nil
	}
	return errInvalidHeartbeatState
}

func (ch *Channel) writeMessage(body []byte) error {
	if err := ch.conn.SetWriteDeadline(time.Now().Add(ch.config.WriteTimeout)); err != nil {
		return err
	}
	return ch.conn.WriteMessage(websocket.BinaryMessage, body)
}

// Close closes the WebSocket channel.
func (ch *Channel) Close() error { return ch.closeWithReason("channel closed") }

func (ch *Channel) closeWithReason(reason string) error {
	ch.markClosed()
	err := ch.closeConnection(reason)
	ch.cancel()
	return err
}

func (ch *Channel) closeConnection(reason string) error {
	ch.connOnce.Do(func() {
		frame := formatCloseFrame(reason)
		_ = ch.conn.WriteControl(websocket.CloseMessage, frame, time.Now().Add(ch.config.WriteTimeout))
		ch.connErr = ch.conn.Close()
	})
	return ch.connErr
}

func (ch *Channel) abort() error {
	ch.markClosed()
	err := ch.conn.Close()
	ch.cancel()
	return err
}

func (ch *Channel) finishWriter(err error) {
	ch.markClosed()
	err = errors.Join(err, ch.closeConnection("writer stopped"))
	ch.cancel()
	ch.writerErr = err
	close(ch.writerDone)
}

func (ch *Channel) writerError() error {
	err := ch.writerErr
	if err != nil {
		return err
	}
	return network.ErrConnectionClosed
}

// closeOnContext hard-closes the socket after ctx cancel; cancel itself is already done.
func (ch *Channel) closeOnContext() {
	ch.markClosed()
	_ = ch.conn.Close()
}

func (ch *Channel) markClosed() {
	ch.sendMu.Lock()
	ch.closed.Store(true)
	ch.sendMu.Unlock()
}

func isNetworkClosedError(err error) bool {
	if errors.Is(err, network.ErrConnectionClosed) || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) ||
		errors.Is(err, websocket.ErrCloseSent) || websocket.IsCloseError(err, websocket.CloseGoingAway,
		websocket.CloseNormalClosure, websocket.CloseAbnormalClosure, websocket.CloseMessageTooBig) {
		return true
	}
	return false
}

func warnUnexpectedNetworkError(message, connID string, err error) {
	if isNetworkClosedError(err) {
		return
	}
	slog.Warn(message, "conn_id", connID, "error", err)
}

func formatCloseFrame(reason string) []byte {
	reason = strings.ToValidUTF8(reason, "")
	if len(reason) > maxCloseReasonSize {
		reason = strings.ToValidUTF8(reason[:maxCloseReasonSize], "")
	}
	return websocket.FormatCloseMessage(websocket.CloseNormalClosure, reason)
}

var (
	_ network.Connection         = (*Channel)(nil)
	_ network.PreparedConnection = (*Channel)(nil)
)
