// Package inbound 协调认证屏障、业务 FIFO 和独立心跳的生命周期。
package inbound

import (
	"context"
	"errors"
	"log/slog"
	"runtime/debug"

	"yola/api/protocol/v1"
	"yola/network"
)

// ErrQueueFull 表示连接的业务等待队列已满，调用方须关闭连接。
var ErrQueueFull = errors.New("network: request queue full")

// Dispatcher 由连接读循环独占提交和停止，最多启动一个业务执行协程。
type Dispatcher struct {
	ctx      context.Context
	cancel   context.CancelFunc
	conn     network.Connection
	invoke   network.Invoker
	capacity int
	requests chan *v1.Proto
	done     chan struct{}
}

// New 创建尚未认证的调度器；此时所有消息仍同步执行。
func New(ctx context.Context, conn network.Connection, invoke network.Invoker, capacity int) *Dispatcher {
	ctx, cancel := context.WithCancel(ctx)
	return &Dispatcher{ctx: ctx, cancel: cancel, conn: conn, invoke: invoke, capacity: capacity}
}

// Handle 由读循环串行调用；成功认证后只将非心跳消息交给业务 FIFO。
func (d *Dispatcher) Handle(message *v1.Proto) error {
	if err := d.ctx.Err(); err != nil {
		return err
	}
	if d.requests == nil {
		reply, err := d.invoke(d.ctx, message)
		if err != nil {
			return err
		}
		authenticated := reply.Op == v1.OpAuthReply && reply.Code == 0
		if err := d.conn.SendProto(reply); err != nil {
			return err
		}
		if authenticated {
			d.requests = make(chan *v1.Proto, d.capacity)
			d.done = make(chan struct{})
			go d.run()
		}
		return nil
	}
	if message.Op == v1.OpHeartbeat {
		return d.deliver(message)
	}
	select {
	case d.requests <- message:
		return nil
	default:
		return ErrQueueFull
	}
}

// Stop 取消在途处理、丢弃待执行消息并等待业务协程；返回后才可调用 handler.Close。
func (d *Dispatcher) Stop() {
	d.cancel()
	if d.done != nil {
		<-d.done
	}
	d.requests = nil
}

func (d *Dispatcher) run() {
	defer close(d.done)
	defer func() {
		if value := recover(); value != nil {
			slog.ErrorContext(d.ctx, "[network] request panic", "conn_id", d.conn.ConnID(), "value", value, "stack", string(debug.Stack()))
			d.cancel()
			// 连接关闭解阻读循环，读循环仍负责等待本协程和执行 handler.Close。
			if err := d.conn.Close(); err != nil {
				slog.WarnContext(d.ctx, "[network] close after panic failed", "conn_id", d.conn.ConnID(), "error", err)
			}
		}
	}()
	for {
		select {
		case <-d.ctx.Done():
			return
		case message := <-d.requests:
			if d.ctx.Err() != nil {
				return
			}
			if err := d.deliver(message); err != nil {
				if d.ctx.Err() == nil {
					slog.WarnContext(d.ctx, "[network] request failed", "conn_id", d.conn.ConnID(), "error", err)
				}
				d.cancel()
				if err := d.conn.Close(); err != nil {
					slog.WarnContext(d.ctx, "[network] close after request failed", "conn_id", d.conn.ConnID(), "error", err)
				}
				return
			}
		}
	}
}

func (d *Dispatcher) deliver(message *v1.Proto) error {
	reply, err := d.invoke(d.ctx, message)
	if err != nil {
		return err
	}
	return d.conn.SendProto(reply)
}
