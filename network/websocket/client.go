package websocket

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"yola/api/protocol/v1"
	"yola/internal/queue"
	"yola/network"
	"yola/network/internal/auth"
	"yola/network/internal/heartbeat"
	"yola/network/internal/request"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
)

var (
	// ErrNotConnected indicates that no writable channel is established.
	ErrNotConnected = errors.New("client: channel not established")
	// ErrInvalidURL indicates that the configured endpoint is invalid.
	ErrInvalidURL = errors.New("client: invalid URL")
	// ErrAuthenticationRejected reports that Gateway rejected the configured credentials.
	ErrAuthenticationRejected = errors.New("client: authentication rejected")
)

// PushHandler handles a server push body.
type PushHandler func(body []byte)

// KickHandler handles an intentional server-side disconnect.
type KickHandler func(code int32)

// Client is a WebSocket client.
type Client struct {
	channel        *Channel
	requests       request.Tracker
	pushHandlers   map[int32]PushHandler
	kickHandler    KickHandler
	disconnectFunc func(*Channel)
	callbacks      *queue.Queue
	closeOnce      sync.Once
	pingInterval   time.Duration
	requestTimeout time.Duration
}

// NewClient creates a client and waits for initial authentication.
func NewClient(ctx context.Context, opts ...ClientOption) (*Client, error) {
	if ctx == nil {
		return nil, errors.New("websocket: context is nil")
	}
	o, err := resolveClientOptions(opts...)
	if err != nil {
		return nil, err
	}
	dialer := websocket.Dialer{HandshakeTimeout: o.timeout, TLSClientConfig: o.tlsConf}
	conn, response, err := dialer.DialContext(ctx, o.endpoint, nil)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		return nil, err
	}
	pending, err := o.authenticate(ctx, conn)
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
		return nil, errors.New("websocket: initial callbacks exceed callback queue capacity")
	}
	c := &Client{
		channel:        newChannel(ctx, conn, o.codec, *o.channel),
		pushHandlers:   o.pushHandler,
		kickHandler:    o.kickHandler,
		disconnectFunc: o.disconnectFunc,
		callbacks: queue.New(
			queue.WithCapacity(o.callbackQueueSize),
			queue.WithPanicHandler(logCallbackPanic),
		),
		pingInterval:   o.pingInterval,
		requestTimeout: o.requestTimeout,
	}
	initialCallbacks := c.initialCallbacks(o.connectFunc, pending)
	if err := c.callbacks.SubmitBatch(initialCallbacks...); err != nil {
		c.callbacks.Stop()
		_ = c.channel.closeWithReason("client initialization failed")
		return nil, err
	}
	go c.callbacks.Run()
	go c.readLoop()
	go c.heartbeat()
	return c, nil
}

// IsAlive reports whether the client connection is open.
func (c *Client) IsAlive() bool {
	return c != nil && c.channel != nil && !c.channel.Closed()
}

// Channel returns the client connection.
func (c *Client) Channel() *Channel {
	if c == nil {
		return nil
	}
	return c.channel
}

func (c *Client) readLoop() {
	ch := c.channel
	defer func() {
		_ = ch.closeWithReason("read loop stopped")
		<-ch.writerDone
		c.shutdown(ch)
	}()
	for {
		p := new(v1.Proto)
		if err := ch.readFrame(p); err != nil {
			warnUnexpectedNetworkError("[websocket] read failed", ch.ConnID(), err)
			return
		}
		if err := c.dispatchMessage(p); err != nil {
			if errors.Is(err, queue.ErrFull) {
				slog.Warn("[websocket] callback queue full",
					"conn_id", ch.ConnID(),
					"operation", p.Op,
				)
			}
			return
		}
	}
}

func (c *Client) heartbeat() {
	ch := c.channel
	ticker := time.NewTicker(c.pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ch.ctx.Done():
			return
		case <-ticker.C:
			switch ch.heartbeat.Tick() {
			case heartbeat.TickPending:
				continue
			case heartbeat.TickTimeout:
				slog.Warn("[websocket] heartbeat reply timed out")
				_ = ch.closeWithReason("heartbeat timeout")
				return
			}
			if err := ch.sendHeartbeat(); err != nil {
				ch.heartbeat.CancelQueue()
				slog.Warn("[websocket] heartbeat send failed", "error", err)
				_ = ch.closeWithReason("heartbeat failed")
				return
			}
		}
	}
}

// Request sends one request and waits for its response, cancellation, timeout, or disconnect.
func (c *Client) Request(ctx context.Context, command int32, msg proto.Message) ([]byte, int32, error) {
	if ctx == nil {
		return nil, 0, errors.New("websocket: request context is nil")
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
	ch := c.channel
	if ch == nil || ch.Closed() {
		return nil, 0, ErrNotConnected
	}
	message, pending := c.requests.Begin(command, body)
	if err := ch.SendProto(message); err != nil {
		pending.Cancel()
		return nil, 0, err
	}
	return pending.Wait(ctx)
}

func (c *Client) dispatchMessage(p *v1.Proto) error {
	switch p.Op {
	case v1.OpResponse:
		c.requests.Resolve(p)
	case v1.OpPush:
		if handler := c.pushHandlers[p.Cmd]; handler != nil {
			return c.callbacks.Submit(func() { handler(p.Body) })
		}
	case v1.OpHeartbeatReply:
		if !c.channel.heartbeat.Reply() {
			slog.Warn("[websocket] unexpected heartbeat reply")
			_ = c.channel.closeWithReason("unexpected heartbeat reply")
			return errors.New("websocket: unexpected heartbeat reply")
		}
	case v1.OpHeartbeat:
		slog.Warn("[websocket] unexpected inbound heartbeat")
	case v1.OpKick:
		var kick func()
		if c.kickHandler != nil {
			kick = func() { c.kickHandler(p.Code) }
		}
		c.shutdownWith(c.channel, kick)
		return network.ErrConnectionClosed
	default:
		slog.Warn("[websocket] unknown incoming operation", "operation", p.Op)
	}
	return nil
}

// Close closes the client connection.
func (c *Client) Close() {
	if c == nil {
		return
	}
	c.shutdown(c.channel)
}

func (c *Client) shutdown(ch *Channel) {
	c.shutdownWith(ch, nil)
}

func (c *Client) shutdownWith(ch *Channel, beforeDisconnect func()) {
	var finishCallbacks func()
	c.closeOnce.Do(func() {
		terminals := make([]func(), 0, 2)
		if beforeDisconnect != nil {
			terminals = append(terminals, beforeDisconnect)
		}
		if c.disconnectFunc != nil {
			terminals = append(terminals, func() { c.disconnectFunc(ch) })
		}
		if c.callbacks != nil {
			finishCallbacks = c.callbacks.BeginTermination(terminals...)
		}
		if ch != nil {
			_ = ch.closeWithReason("client closed")
		}
		c.requests.Fail(ErrNotConnected)
	})
	if finishCallbacks != nil {
		finishCallbacks()
	}
}

func logCallbackPanic(value any, stack []byte) {
	slog.Error("[websocket] callback panic",
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

func (c *Client) initialCallbacks(connect func(*Channel), pending []*v1.Proto) []func() {
	callbacks := make([]func(), 0, len(pending)+callbackSlot(connect != nil))
	if connect != nil {
		callbacks = append(callbacks, func() { connect(c.channel) })
	}
	for _, message := range pending {
		handler := c.pushHandlers[message.Cmd]
		if handler == nil {
			continue
		}
		body := message.Body
		callbacks = append(callbacks, func() { handler(body) })
	}
	return callbacks
}

func (o *clientOptions) authenticate(ctx context.Context, conn *websocket.Conn) ([]*v1.Proto, error) {
	authCtx, cancel := context.WithTimeout(ctx, o.timeout)
	conn.SetReadLimit(v1.MaxProtoSize)
	deadline, _ := authCtx.Deadline()
	if err := conn.SetWriteDeadline(deadline); err != nil {
		cancel()
		return nil, err
	}
	if err := conn.SetReadDeadline(deadline); err != nil {
		cancel()
		return nil, err
	}
	// Gorilla allows Close, but not deadline setters, to run concurrently with reads and writes.
	stopInterrupt := context.AfterFunc(authCtx, func() {
		_ = conn.Close()
	})
	defer func() {
		if stopInterrupt() {
			_ = conn.SetWriteDeadline(time.Time{})
			_ = conn.SetReadDeadline(time.Time{})
		}
		cancel()
	}()
	body, err := proto.Marshal(&v1.ClientAuthReq{ServiceName: o.serviceName, Token: o.token})
	if err != nil {
		return nil, err
	}
	frame, err := marshalFrame(o.codec, &v1.Proto{Op: v1.OpAuth, Body: body})
	if err != nil {
		return nil, err
	}
	if err = conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
		return nil, network.AuthenticationIOError(authCtx, err)
	}
	return o.readAuthenticationReply(authCtx, conn)
}

func (o *clientOptions) readAuthenticationReply(authCtx context.Context, conn *websocket.Conn) ([]*v1.Proto, error) {
	return auth.ReadReply(
		"websocket",
		ErrAuthenticationRejected,
		o.callbackQueueSize,
		func() (*v1.Proto, error) {
			messageType, frame, readErr := conn.ReadMessage()
			if readErr != nil {
				return nil, network.AuthenticationIOError(authCtx, readErr)
			}
			if messageType != websocket.BinaryMessage {
				return nil, errors.New("websocket: invalid authentication response")
			}
			reply := new(v1.Proto)
			if err := unmarshalFrame(o.codec, frame, reply); err != nil {
				return nil, err
			}
			return reply, nil
		},
	)
}
