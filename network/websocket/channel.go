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

const _maxCloseReasonSize = 123

var _readFrameBuffers = sync.Pool{
	New: func() any { return new([v1.MaxProtoSize]byte) },
}

// Channel 持有一条 Gorilla WebSocket 连接。
type Channel struct {
	connID     string
	remoteAddr string
	conn       *websocket.Conn
	codec      encoding.Codec
	config     ChannelConfig
	ctx        context.Context
	cancel     context.CancelFunc
	outbound   chan outboundFrame
	// heartbeat 归当前连接所有，旧回复不能确认替换后的 Channel 心跳。
	heartbeat heartbeat.State
	closed    atomic.Bool
	sendMu    sync.Mutex
	connOnce  sync.Once
	connErr   error
	// writerErr 通过关闭 writerDone 发布，仅在该 channel 关闭后读取。
	writerErr           error
	writerDone          chan struct{}
	pendingPayloadBytes atomic.Int64
	queueDropped        atomic.Uint64
}

type outboundFrame struct {
	body         []byte
	payloadBytes int64
	heartbeat    bool
	// done 仅在最后一帧非 nil，也标识 writer 终止。
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

// ConnID 返回物理连接标识。
func (ch *Channel) ConnID() string { return ch.connID }

// RemoteAddr 返回远端网络地址。
func (ch *Channel) RemoteAddr() string { return ch.remoteAddr }

// Closed 判断 channel 是否已关闭。
func (ch *Channel) Closed() bool { return ch.closed.Load() }

// SendStats 返回连接发送队列的只读快照。
func (ch *Channel) SendStats() network.SendStats {
	return network.SendStats{
		QueueDepth:          len(ch.outbound),
		QueueCapacity:       cap(ch.outbound),
		PendingPayloadBytes: ch.pendingPayloadBytes.Load(),
		QueueDropped:        ch.queueDropped.Load(),
		Closed:              ch.closed.Load(),
	}
}

// SendProto 将不可变 Proto 编码为 Binary frame 入队；输入遵守 network.Connection 的所有权契约。
func (ch *Channel) SendProto(p *v1.Proto) error {
	return ch.enqueueProto(p, false)
}

// SendPrepared 为 Gateway fanout 排入共用的默认 protobuf 编码。
// 自定义 codec 保持 SendProto 行为：同名 codec 不保证产生相同协议 bytes。
func (ch *Channel) SendPrepared(message *network.PreparedProto) error {
	if message == nil || message.Message() == nil {
		return errNilPayload
	}
	if !canOptimizeFrames(ch.codec) {
		return ch.SendProto(message.Message())
	}
	return ch.enqueueFrame(len(message.Message().Body), false, func() ([]byte, error) {
		body, err := message.Marshal()
		if err != nil {
			return nil, err
		}
		return body, validateFrameBody(body)
	})
}

// sendHeartbeat 标记心跳帧，由 writeLoop 推进回复超时状态，
// 入队路径不推进该状态。
func (ch *Channel) sendHeartbeat() error {
	return ch.enqueueProto(&v1.Proto{Op: v1.OpHeartbeat}, true)
}

func (ch *Channel) enqueueProto(p *v1.Proto, heartbeat bool) error {
	if p == nil {
		return errNilPayload
	}
	return ch.enqueueFrame(len(p.Body), heartbeat, func() ([]byte, error) {
		return marshalFrame(ch.codec, p)
	})
}

func (ch *Channel) enqueueFrame(payloadBytes int, heartbeat bool, encode func() ([]byte, error)) error {
	ch.sendMu.Lock()
	defer ch.sendMu.Unlock()
	if ch.closed.Load() {
		return network.ErrConnectionClosed
	}
	if len(ch.outbound) == cap(ch.outbound) {
		ch.queueDropped.Add(1)
		return network.ErrSendQueueFull
	}
	body, err := encode()
	if err != nil {
		return err
	}
	// sendMu 串行化生产者，因此上面的容量检查仍然有效。
	ch.pendingPayloadBytes.Add(int64(payloadBytes))
	ch.outbound <- outboundFrame{body: body, payloadBytes: int64(payloadBytes), heartbeat: heartbeat}
	return nil
}

// CloseWithProto 写入最后一个 Binary 帧后关闭连接。
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
	frame := outboundFrame{body: body, payloadBytes: int64(len(p.Body)), done: make(chan error, 1)}
	ch.pendingPayloadBytes.Add(frame.payloadBytes)
	select {
	case ch.outbound <- frame:
	case <-ch.writerDone:
		ch.pendingPayloadBytes.Add(-frame.payloadBytes)
		return ch.writerError()
	case <-ctx.Done():
		ch.pendingPayloadBytes.Add(-frame.payloadBytes)
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
	buffer := _readFrameBuffers.Get().(*[v1.MaxProtoSize]byte)
	defer _readFrameBuffers.Put(buffer)
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
			ch.pendingPayloadBytes.Add(-frame.payloadBytes)
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

// Close 关闭 WebSocket channel。
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

// closeOnContext 在 ctx 取消后强制关闭 socket；此时取消信号已发出。
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
	return errors.Is(err, network.ErrConnectionClosed) || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) ||
		errors.Is(err, websocket.ErrCloseSent) || websocket.IsCloseError(err, websocket.CloseGoingAway,
		websocket.CloseNormalClosure, websocket.CloseAbnormalClosure, websocket.CloseMessageTooBig)
}

func warnUnexpectedNetworkError(message, connID string, err error) {
	if isNetworkClosedError(err) {
		return
	}
	slog.Warn(message, "conn_id", connID, "error", err)
}

func formatCloseFrame(reason string) []byte {
	reason = strings.ToValidUTF8(reason, "")
	if len(reason) > _maxCloseReasonSize {
		reason = strings.ToValidUTF8(reason[:_maxCloseReasonSize], "")
	}
	return websocket.FormatCloseMessage(websocket.CloseNormalClosure, reason)
}

var (
	_ network.Connection         = (*Channel)(nil)
	_ network.PreparedConnection = (*Channel)(nil)
)
