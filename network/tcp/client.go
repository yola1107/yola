package tcp

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"yola/api/protocol/v1"
	"yola/internal/queue"
	"yola/network"
	"yola/network/internal/auth"
	"yola/network/internal/heartbeat"
	"yola/network/internal/request"

	"github.com/go-kratos/kratos/v3/encoding"
	"google.golang.org/protobuf/proto"
)

// PushHandler handles a server push body.
type PushHandler func(body []byte)

// KickHandler handles an intentional server-side disconnect.
type KickHandler func(code int32)

// ErrAuthenticationRejected reports that Gateway rejected the configured credentials.
var ErrAuthenticationRejected = errors.New("tcp client authentication rejected")

const clientSendQueueSize = 100

type Client struct {
	pushChan       chan *v1.Proto
	pushHandlers   map[int32]PushHandler
	kickHandler    KickHandler
	disconnectFunc func()
	requests       request.Tracker
	conn           net.Conn
	codec          encoding.Codec
	done           chan struct{}
	sendMu         sync.Mutex
	closeOnce      sync.Once
	callbacks      *queue.Queue
	heartbeat      heartbeat.State
	pingInterval   time.Duration
	readTimeout    time.Duration
	writeTimeout   time.Duration
	requestTimeout time.Duration
}

// NewClient creates a TCP client and waits for initial authentication.
func NewClient(ctx context.Context, opts ...ClientOption) (*Client, error) {
	if ctx == nil {
		return nil, errors.New("tcp: context is nil")
	}
	o, err := resolveClientOptions(opts...)
	if err != nil {
		return nil, err
	}
	conn, err := dialGateway(ctx, o.endpoint, o.tlsConf)
	if err != nil {
		return nil, err
	}
	wr := bufio.NewWriter(conn)
	rd := bufio.NewReader(conn)
	pending, err := o.authenticate(ctx, conn, rd, wr)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if len(pending)+callbackSlot(o.connectFunc != nil) > o.callbackQueueSize {
		_ = conn.Close()
		return nil, errors.New("tcp: initial callbacks exceed callback queue capacity")
	}
	c := &Client{
		conn:           conn,
		codec:          o.codec,
		pushChan:       make(chan *v1.Proto, clientSendQueueSize),
		pushHandlers:   o.pushHandlers,
		kickHandler:    o.kickHandler,
		disconnectFunc: o.disconnectFunc,
		done:           make(chan struct{}),
		callbacks: queue.New(
			queue.WithCapacity(o.callbackQueueSize),
			queue.WithPanicHandler(logCallbackPanic),
		),
		pingInterval:   o.pingInterval,
		readTimeout:    o.readTimeout,
		writeTimeout:   o.writeTimeout,
		requestTimeout: o.requestTimeout,
	}
	initialCallbacks := c.initialCallbacks(o.connectFunc, pending)
	if err := c.callbacks.SubmitBatch(initialCallbacks...); err != nil {
		c.callbacks.Stop()
		_ = conn.Close()
		return nil, errors.New("tcp: initial callbacks are unavailable")
	}
	go c.callbacks.Run()
	go c.readLoop(rd)
	go c.writeLoop(wr)
	go c.sendHeart()
	go func() {
		select {
		case <-ctx.Done():
			c.shutdown()
		case <-c.done:
		}
	}()
	return c, nil
}

func dialGateway(ctx context.Context, endpoint string, c *tls.Config) (net.Conn, error) {
	dialer := new(net.Dialer)
	if c == nil {
		return dialer.DialContext(ctx, "tcp", endpoint)
	}
	return (&tls.Dialer{NetDialer: dialer, Config: c}).DialContext(ctx, "tcp", endpoint)
}

// Request sends one request and waits for its response, cancellation, timeout, or disconnect.
func (c *Client) Request(ctx context.Context, command int32, msg proto.Message) ([]byte, int32, error) {
	if ctx == nil {
		return nil, 0, errors.New("tcp: request context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	body, err := proto.Marshal(msg)
	if err != nil {
		return nil, 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.requestTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	message, pending := c.requests.Begin(command, body)
	if err := c.send(message); err != nil {
		pending.Cancel()
		return nil, 0, err
	}
	return pending.Wait(ctx)
}

func (c *Client) Close() {
	c.shutdown()
}

func (c *Client) sendHeart() {
	ticker := time.NewTicker(c.pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			switch c.heartbeat.Tick() {
			case heartbeat.TickPending:
				continue
			case heartbeat.TickTimeout:
				slog.Warn("[tcp] heartbeat reply timed out")
				c.shutdown()
				return
			}
			if err := c.send(&v1.Proto{Op: v1.OpHeartbeat}); err != nil {
				c.heartbeat.CancelQueue()
				slog.Warn("[tcp] heartbeat send failed", "error", err)
				c.shutdown()
				return
			}
		}
	}
}

func (c *Client) readLoop(rd *bufio.Reader) {
	for {
		if err := c.conn.SetReadDeadline(time.Now().Add(c.readTimeout)); err != nil {
			slog.Warn("[tcp] set read deadline failed", "error", err)
			c.shutdown()
			return
		}
		p := &v1.Proto{}
		if err := readFrame(rd, c.codec, p); err != nil {
			if !isConnectionClosedError(err) {
				slog.Warn("[tcp] read failed", "error", err)
			}
			c.shutdown()
			return
		}
		if !c.handleIncoming(p) {
			return
		}
	}
}

func (c *Client) handleIncoming(p *v1.Proto) bool {
	switch p.Op {
	case v1.OpHeartbeatReply:
		if !c.heartbeat.Reply() {
			slog.Warn("[tcp] unexpected heartbeat reply")
			c.shutdown()
			return false
		}
	case v1.OpPush:
		handler := c.pushHandlers[p.Cmd]
		if handler == nil {
			slog.Warn("[tcp] push handler not found", "command", p.Cmd)
			return true
		}
		if err := c.submitCallback("push", func() { handler(p.Body) }); err != nil {
			c.shutdown()
			return false
		}

	case v1.OpResponse:
		if !c.requests.Resolve(p) {
			slog.Debug("[tcp] pending request not found", "sequence", p.Seq)
		}

	case v1.OpKick:
		var kick func()
		if c.kickHandler != nil {
			kick = func() { c.kickHandler(p.Code) }
		}
		c.shutdownWith(kick)
		return false

	default:
		slog.Warn("[tcp] unknown incoming operation", "operation", p.Op)
	}
	return true
}

func (c *Client) writeLoop(wr *bufio.Writer) {
	for {
		var p *v1.Proto
		select {
		case <-c.done:
			return
		case p = <-c.pushChan:
		}
		heartbeat := p.Op == v1.OpHeartbeat
		if heartbeat && !c.heartbeat.BeginWrite() {
			slog.Warn("[tcp] heartbeat write has invalid state")
			c.shutdown()
			return
		}
		if err := c.writeMessage(wr, p); err != nil {
			slog.Warn("[tcp] write failed", "error", err)
			c.shutdown()
			return
		}
		if heartbeat && !c.heartbeat.FinishWrite() {
			slog.Warn("[tcp] heartbeat completion has invalid state")
			c.shutdown()
			return
		}
	}
}

func (c *Client) writeMessage(wr *bufio.Writer, p *v1.Proto) error {
	if err := c.conn.SetWriteDeadline(time.Now().Add(c.writeTimeout)); err != nil {
		return fmt.Errorf("set write deadline: %w", err)
	}
	if err := writeFrame(wr, c.codec, p); err != nil {
		return fmt.Errorf("encode frame: %w", err)
	}
	if err := wr.Flush(); err != nil {
		return fmt.Errorf("flush frame: %w", err)
	}
	return nil
}

func (c *Client) send(p *v1.Proto) error {
	select {
	case <-c.done:
		return net.ErrClosed
	default:
	}
	if c.codec == nil {
		return errors.New("tcp: client codec is nil")
	}
	if err := validateFrame(c.codec, p); err != nil {
		return err
	}
	switch p.Op {
	case v1.OpHeartbeat, v1.OpRequest:
	default:
		return fmt.Errorf("tcp: unsupported outgoing operation %d", p.Op)
	}
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	select {
	case <-c.done:
		return net.ErrClosed
	default:
	}
	select {
	case c.pushChan <- p:
		return nil
	default:
		return network.ErrSendQueueFull
	}
}

func (c *Client) shutdown() {
	c.shutdownWith(nil)
}

func (c *Client) shutdownWith(beforeDisconnect func()) {
	var finishCallbacks func()
	c.closeOnce.Do(func() {
		terminals := make([]func(), 0, 2)
		if beforeDisconnect != nil {
			terminals = append(terminals, beforeDisconnect)
		}
		if c.disconnectFunc != nil {
			terminals = append(terminals, c.disconnectFunc)
		}
		if c.callbacks != nil {
			finishCallbacks = c.callbacks.BeginTermination(terminals...)
		}
		c.sendMu.Lock()
		if c.done != nil {
			close(c.done)
		}
		c.sendMu.Unlock()
		if c.conn != nil {
			_ = c.conn.Close()
		}
		c.requests.Fail(net.ErrClosed)
	})
	if finishCallbacks != nil {
		finishCallbacks()
	}
}

func (c *Client) submitCallback(kind string, callback func()) error {
	err := c.callbacks.Submit(callback)
	if errors.Is(err, queue.ErrFull) {
		slog.Warn("[tcp] callback queue full", "callback", kind)
	}
	return err
}

func logCallbackPanic(value any, stack []byte) {
	slog.Error("[tcp] client callback panic",
		"value", value,
		"stack", string(stack),
	)
}

func callbackSlot(enabled bool) int {
	if enabled {
		return 1
	}
	return 0
}

func (c *Client) initialCallbacks(connect func(), pending []*v1.Proto) []func() {
	callbacks := make([]func(), 0, len(pending)+callbackSlot(connect != nil))
	if connect != nil {
		callbacks = append(callbacks, connect)
	}
	for _, message := range pending {
		handler := c.pushHandlers[message.Cmd]
		if handler == nil {
			slog.Warn("[tcp] push handler not found", "command", message.Cmd)
			continue
		}
		body := message.Body
		callbacks = append(callbacks, func() { handler(body) })
	}
	return callbacks
}

func (o *clientOptions) authenticate(ctx context.Context, conn net.Conn, rd *bufio.Reader, wr *bufio.Writer) ([]*v1.Proto, error) {
	authCtx, cancel := context.WithTimeout(ctx, defaultAuthenticationTimeout)
	deadline, _ := authCtx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		cancel()
		return nil, err
	}
	// Closing is the portable way to interrupt I/O when a context without a deadline is canceled.
	stopInterrupt := context.AfterFunc(authCtx, func() {
		_ = conn.Close()
	})
	defer func() {
		if stopInterrupt() {
			_ = conn.SetDeadline(time.Time{})
		}
		cancel()
	}()
	body, err := proto.Marshal(&v1.ClientAuthReq{ServiceName: o.serviceName, Token: o.token})
	if err != nil {
		return nil, err
	}
	if err := writeFrame(wr, o.codec, &v1.Proto{Op: v1.OpAuth, Body: body}); err != nil {
		return nil, network.AuthenticationIOError(authCtx, err)
	}
	if err := wr.Flush(); err != nil {
		return nil, network.AuthenticationIOError(authCtx, err)
	}
	return o.readAuthenticationReply(authCtx, rd)
}

func (o *clientOptions) readAuthenticationReply(authCtx context.Context, rd *bufio.Reader) ([]*v1.Proto, error) {
	return auth.ReadReply(
		"tcp",
		ErrAuthenticationRejected,
		o.callbackQueueSize,
		func() (*v1.Proto, error) {
			reply := new(v1.Proto)
			if err := readFrame(rd, o.codec, reply); err != nil {
				return nil, network.AuthenticationIOError(authCtx, err)
			}
			return reply, nil
		},
	)
}
