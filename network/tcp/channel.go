package tcp

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"

	"yola/api/protocol/v1"
	"yola/network"

	"github.com/go-kratos/kratos/v3/encoding"
)

type channelEvent struct {
	proto *v1.Proto
	// done is non-nil only for the final frame, so it also marks writer termination.
	done chan error
}

type channel struct {
	outbound   chan channelEvent
	closing    chan struct{}
	stop       chan struct{}
	writerDone chan struct{}

	connID string
	codec  encoding.Codec

	ip                  string
	cancel              context.CancelFunc
	mu                  sync.RWMutex
	replyWG             sync.WaitGroup
	closed              bool
	stopped             bool
	writerErr           error
	pendingPayloadBytes atomic.Int64
	queueDropped        atomic.Uint64
}

func (c *channel) closeWithProto(ctx context.Context, p *v1.Proto) error {
	if err := validateFrame(c.codec, p); err != nil {
		return err
	}
	event := channelEvent{proto: p, done: make(chan error, 1)}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return network.ErrConnectionClosed
	}
	c.closed = true
	close(c.closing)
	c.mu.Unlock()
	c.replyWG.Wait()
	size := int64(len(p.Body))
	c.pendingPayloadBytes.Add(size)
	select {
	case c.outbound <- event:
	case <-ctx.Done():
		c.pendingPayloadBytes.Add(-size)
		c.close()
		return ctx.Err()
	case <-c.stop:
		c.pendingPayloadBytes.Add(-size)
		return c.writerError()
	case <-c.writerDone:
		c.pendingPayloadBytes.Add(-size)
		return c.writerError()
	}
	return c.waitFinal(ctx, event.done)
}

func (c *channel) waitFinal(ctx context.Context, result <-chan error) error {
	select {
	case err := <-result:
		return err
	default:
	}
	contextDone := false
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		contextDone = true
	case <-c.stop:
	case <-c.writerDone:
	}
	select {
	case err := <-result:
		return err
	default:
	}
	if contextDone {
		c.close()
		return ctx.Err()
	}
	return c.writerError()
}

func newChannel(queueSize int, codec encoding.Codec) *channel {
	return &channel{
		outbound:   make(chan channelEvent, queueSize),
		closing:    make(chan struct{}),
		stop:       make(chan struct{}),
		writerDone: make(chan struct{}),
		codec:      codec,
	}
}

func (c *channel) push(p *v1.Proto) error {
	if err := validateFrame(c.codec, p); err != nil {
		return err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.closed {
		return network.ErrConnectionClosed
	}
	size := int64(len(p.Body))
	c.pendingPayloadBytes.Add(size)
	select {
	case c.outbound <- channelEvent{proto: p}:
		return nil
	default:
		c.pendingPayloadBytes.Add(-size)
		c.queueDropped.Add(1)
		return network.ErrSendQueueFull
	}
}

func (c *channel) reply(ctx context.Context, p *v1.Proto) error {
	if err := validateFrame(c.codec, p); err != nil {
		return err
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return c.waitClosed(ctx)
	}
	c.replyWG.Add(1)
	c.mu.Unlock()
	size := int64(len(p.Body))
	c.pendingPayloadBytes.Add(size)
	select {
	case c.outbound <- channelEvent{proto: p}:
		c.replyWG.Done()
		return nil
	case <-ctx.Done():
		c.pendingPayloadBytes.Add(-size)
		c.replyWG.Done()
		return ctx.Err()
	case <-c.closing:
		c.pendingPayloadBytes.Add(-size)
		c.replyWG.Done()
		return c.waitClosed(ctx)
	}
}

func (c *channel) waitClosed(ctx context.Context) error {
	select {
	case <-c.stop:
		return network.ErrConnectionClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *channel) next() (channelEvent, bool) {
	select {
	case event := <-c.outbound:
		c.pendingPayloadBytes.Add(-int64(len(event.proto.Body)))
		return event, true
	case <-c.stop:
		return channelEvent{}, false
	}
}

func (c *channel) close() {
	c.mu.Lock()
	c.stopLocked()
	cancel := c.cancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (c *channel) isClosed() bool {
	c.mu.RLock()
	closed := c.closed
	c.mu.RUnlock()
	return closed
}

func (c *channel) stopLocked() {
	if !c.closed {
		c.closed = true
		close(c.closing)
	}
	if !c.stopped {
		c.stopped = true
		close(c.stop)
	}
}

func (c *channel) finishWriter(err error) {
	c.mu.Lock()
	c.writerErr = err
	c.stopLocked()
	close(c.writerDone)
	cancel := c.cancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (c *channel) writerError() error {
	c.mu.RLock()
	err := c.writerErr
	c.mu.RUnlock()
	if err != nil {
		return err
	}
	return network.ErrConnectionClosed
}

func isConnectionClosedError(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) ||
		errors.Is(err, network.ErrConnectionClosed)
}
