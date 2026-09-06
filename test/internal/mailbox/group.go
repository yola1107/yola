package mailbox

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
)

// Group preserves FIFO execution per mailbox while sharing a bounded worker set.
type Group struct {
	mailboxes []*mailboxQueue
	ready     chan *mailboxQueue
	stopping  chan struct{}
	done      chan struct{}
	finished  chan struct{}
	workers   int
	batch     int
	capacity  int
	mu        sync.RWMutex
	started   bool
	stopped   bool
	pendingMu sync.Mutex
	pending   int64
	idle      *sync.Cond
	wg        sync.WaitGroup
}

type mailboxQueue struct {
	group     *Group
	mu        sync.Mutex
	queue     []func()
	scheduled bool
	running   atomic.Int32
	space     chan struct{}
}

func NewGroup(mailboxCount, workerCount, queueSize, batchSize int) (*Group, error) {
	if mailboxCount <= 0 || workerCount <= 0 || queueSize <= 0 || batchSize <= 0 {
		return nil, fmt.Errorf("mailbox group counts and sizes must be positive")
	}
	g := &Group{
		mailboxes: make([]*mailboxQueue, mailboxCount),
		ready:     make(chan *mailboxQueue, mailboxCount),
		stopping:  make(chan struct{}),
		done:      make(chan struct{}),
		finished:  make(chan struct{}),
		workers:   workerCount,
		batch:     batchSize,
		capacity:  queueSize,
	}
	g.idle = sync.NewCond(&g.pendingMu)
	for i := range g.mailboxes {
		g.mailboxes[i] = &mailboxQueue{group: g, space: make(chan struct{})}
	}
	return g, nil
}

func (g *Group) Start() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.stopped {
		return ErrStopped
	}
	if g.started {
		return nil
	}
	g.started = true
	g.wg.Add(g.workers)
	for range g.workers {
		go g.run()
	}
	return nil
}

// Stop rejects new jobs and waits for accepted jobs and workers to finish or ctx cancellation.
func (g *Group) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	g.mu.Lock()
	if !g.stopped {
		g.stopped = true
		started := g.started
		close(g.stopping)
		if started {
			go g.finishStop()
		} else {
			close(g.done)
			close(g.finished)
		}
	}
	finished := g.finished
	g.mu.Unlock()
	select {
	case <-finished:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("stop mailbox group: %w", ctx.Err())
	}
}

func (g *Group) finishStop() {
	g.pendingMu.Lock()
	for g.pending != 0 {
		g.idle.Wait()
	}
	g.pendingMu.Unlock()
	close(g.done)
	g.wg.Wait()
	close(g.finished)
}

func (g *Group) Executor(index int) Executor {
	if index < 0 || index >= len(g.mailboxes) {
		return nil
	}
	return g.mailboxes[index]
}

// Post waits for bounded queue capacity and returns when the job is accepted or the group stops.
func (g *Group) Post(ctx context.Context, index int, job func()) error {
	if index < 0 || index >= len(g.mailboxes) {
		return fmt.Errorf("mailbox index %d out of range", index)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	mailbox := g.mailboxes[index]
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := mailbox.post(context.Background(), job)
		if err != ErrFull {
			return err
		}
		if err = mailbox.waitForSpace(ctx); err != nil {
			return err
		}
	}
}

// PostAndWait waits for queue capacity, cancels work that has not started, and
// waits for an action that has already started to finish.
func (g *Group) PostAndWait(ctx context.Context, index int, job func() error) error {
	if index < 0 || index >= len(g.mailboxes) {
		return fmt.Errorf("mailbox index %d out of range", index)
	}
	ctx, call, err := newMailboxCall(ctx, job)
	if err != nil {
		return err
	}
	if err = g.Post(ctx, index, call.run); err != nil {
		return err
	}
	return call.wait(ctx)
}

func (g *Group) run() {
	defer g.wg.Done()
	for {
		select {
		case <-g.done:
			return
		case mailbox := <-g.ready:
			mailbox.drain(g.batch)
		}
	}
}

func (mailbox *mailboxQueue) TryPost(job func()) error {
	return mailbox.post(context.Background(), job)
}

func (mailbox *mailboxQueue) post(ctx context.Context, job func()) error {
	if job == nil {
		return ErrNilJob
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	mailbox.group.mu.RLock()
	defer mailbox.group.mu.RUnlock()
	if mailbox.group.stopped {
		return ErrStopped
	}
	if !mailbox.group.started {
		return ErrNotStarted
	}

	mailbox.mu.Lock()
	if len(mailbox.queue) >= mailbox.group.capacity {
		mailbox.mu.Unlock()
		return ErrFull
	}
	mailbox.queue = append(mailbox.queue, func() {
		if ctx.Err() == nil {
			job()
		}
	})
	mailbox.group.changePending(1)
	if !mailbox.scheduled {
		mailbox.scheduled = true
		mailbox.group.ready <- mailbox
	}
	mailbox.mu.Unlock()
	return nil
}

func (mailbox *mailboxQueue) Call(ctx context.Context, job func() error) error {
	ctx, call, err := newMailboxCall(ctx, job)
	if err != nil {
		return err
	}
	err = mailbox.post(context.Background(), call.run)
	if err != nil {
		return err
	}
	return call.wait(ctx)
}

type mailboxCall struct {
	state  atomic.Int32
	result chan error
	job    func() error
}

const (
	callQueued int32 = iota
	callStarted
	callCanceled
)

func newMailboxCall(ctx context.Context, job func() error) (context.Context, *mailboxCall, error) {
	if job == nil {
		return ctx, nil, ErrNilJob
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return ctx, nil, err
	}
	return ctx, &mailboxCall{result: make(chan error, 1), job: job}, nil
}

func (c *mailboxCall) run() {
	if !c.state.CompareAndSwap(callQueued, callStarted) {
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			c.result <- fmt.Errorf("mailbox job panic: %v", recovered)
		}
	}()
	c.result <- c.job()
}

func (c *mailboxCall) wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		if c.state.CompareAndSwap(callQueued, callCanceled) {
			return ctx.Err()
		}
		return <-c.result
	case runErr := <-c.result:
		return runErr
	}
}

func (mailbox *mailboxQueue) Stats() Stats {
	mailbox.mu.Lock()
	queued := len(mailbox.queue)
	mailbox.mu.Unlock()
	return Stats{
		Capacity: mailbox.group.capacity,
		Running:  int(mailbox.running.Load()),
		Free:     mailbox.group.capacity - queued,
	}
}

func (mailbox *mailboxQueue) drain(batch int) {
	mailbox.running.Store(1)
	defer mailbox.running.Store(0)
	for range batch {
		mailbox.mu.Lock()
		if len(mailbox.queue) == 0 {
			mailbox.scheduled = false
			mailbox.mu.Unlock()
			return
		}
		job := mailbox.queue[0]
		mailbox.queue[0] = nil
		mailbox.queue = mailbox.queue[1:]
		mailbox.notifySpaceLocked()
		mailbox.mu.Unlock()
		callSafely(job)
		mailbox.group.changePending(-1)
	}
	mailbox.mu.Lock()
	if len(mailbox.queue) == 0 {
		mailbox.scheduled = false
	} else {
		mailbox.group.ready <- mailbox
	}
	mailbox.mu.Unlock()
}

func (mailbox *mailboxQueue) waitForSpace(ctx context.Context) error {
	mailbox.mu.Lock()
	if len(mailbox.queue) < mailbox.group.capacity {
		mailbox.mu.Unlock()
		return nil
	}
	space := mailbox.space
	mailbox.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-mailbox.group.stopping:
		return ErrStopped
	case <-space:
		return nil
	}
}

func (mailbox *mailboxQueue) notifySpaceLocked() {
	close(mailbox.space)
	mailbox.space = make(chan struct{})
}

func (g *Group) changePending(delta int64) {
	g.pendingMu.Lock()
	g.pending += delta
	if g.pending == 0 {
		g.idle.Broadcast()
	}
	g.pendingMu.Unlock()
}
