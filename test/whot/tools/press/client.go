package press

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"yola/test/internal/mailbox"
	"yola/test/internal/xgo"
	"yola/test/whot/api/v1"
)

type Runner struct {
	conf *LoadTest

	mailboxes *mailbox.Group
	executor  mailbox.Executor
	delays    delayedJobs
	ctx       context.Context
	cancel    context.CancelFunc

	users      sync.Map
	count      atomic.Int32
	nextID     atomic.Int64
	stop       sync.Once
	background sync.WaitGroup

	commandCounts [int(v1.GameCommand_CmdResultPush) + 1]atomic.Uint64
}

func NewRunner(parent context.Context, conf *LoadTest) *Runner {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	mailboxes, err := mailbox.NewGroup(1, 1, 10000, 64)
	if err != nil {
		panic(fmt.Sprintf("create press mailbox: %v", err))
	}
	return &Runner{
		mailboxes: mailboxes,
		executor:  mailboxes.Executor(0),
		conf:      conf,
		ctx:       ctx,
		cancel:    cancel,
	}
}

func (r *Runner) After(delay time.Duration, job func()) bool {
	if job == nil {
		return false
	}
	return r.delays.After(delay, func() {
		err := r.mailboxes.Post(r.ctx, 0, func() {
			if r.ctx.Err() == nil {
				job()
			}
		})
		if err != nil && r.ctx.Err() == nil {
			slog.Warn("deliver press delayed job", "error", err)
		}
	})
}

func (r *Runner) Post(job func()) error {
	return r.executor.TryPost(job)
}

func (r *Runner) GetContext() context.Context {
	return r.ctx
}

func (r *Runner) GetURL() string {
	return r.conf.Press.URL
}

func (r *Runner) GetConfig() Press {
	return r.conf.Press
}

func (r *Runner) Start() error {
	if err := r.mailboxes.Start(); err != nil {
		return err
	}
	started := false
	defer func() {
		if !started {
			r.stopRuntime()
		}
	}()
	if r.conf.Press.Open {
		probe := &User{repo: r, id: r.conf.Press.StartID}
		client, err := probe.connect()
		if err != nil {
			return fmt.Errorf("authenticate press probe: %w", err)
		}
		client.Close()
	}

	interval := time.Duration(r.conf.Press.Interval) * time.Millisecond
	if interval <= 0 {
		return fmt.Errorf("press interval must be positive")
	}
	r.background.Add(1)
	go func() {
		defer r.background.Done()
		r.run(interval)
	}()
	started = true
	slog.Info("start press client", "config", r.conf.Press)
	return nil
}

func (r *Runner) Stop() {
	r.stop.Do(func() {
		r.stopRuntime()
		r.users.Range(func(key, value any) bool {
			value.(*User).Release()
			r.users.Delete(key)
			return true
		})
		r.count.Store(0)
		slog.Info("stop press client")
	})
}

func (r *Runner) stopRuntime() {
	r.cancel()
	r.background.Wait()
	r.delays.Stop()
	if err := r.mailboxes.Stop(context.Background()); err != nil {
		slog.Error("stop press mailboxes", "error", err)
	}
}

func (r *Runner) RecordCommand(command v1.GameCommand) {
	index := int(command)
	if index <= 0 || index >= len(r.commandCounts) {
		return
	}
	r.commandCounts[index].Add(1)
}

func (r *Runner) CommandCount(command v1.GameCommand) uint64 {
	index := int(command)
	if index <= 0 || index >= len(r.commandCounts) {
		return 0
	}
	return r.commandCounts[index].Load()
}

func (r *Runner) CommandCounts() map[string]uint64 {
	counts := make(map[string]uint64, int(v1.GameCommand_CmdResultPush))
	for command := v1.GameCommand_CmdLogin; command <= v1.GameCommand_CmdResultPush; command++ {
		counts[command.String()] = r.commandCounts[int(command)].Load()
	}
	return counts
}

func (r *Runner) Monitor() {
	slog.Info("press client status",
		"mailbox", r.executor.Stats(),
		"target_players", r.conf.Press.Num,
		"current_players", r.count.Load(),
		"command_counts", r.CommandCounts(),
	)
}

func (r *Runner) run(loadInterval time.Duration) {
	loadTicker := time.NewTicker(loadInterval)
	maintenanceTicker := time.NewTicker(15 * time.Second)
	defer loadTicker.Stop()
	defer maintenanceTicker.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-loadTicker.C:
			r.postPeriodic("load", r.Load)
		case <-maintenanceTicker.C:
			r.postPeriodic("maintenance", func() {
				r.Release()
				r.Monitor()
			})
		}
	}
}

func (r *Runner) postPeriodic(name string, job func()) {
	err := r.Post(func() {
		if r.ctx.Err() == nil {
			job()
		}
	})
	if err != nil && r.ctx.Err() == nil {
		slog.Warn("post press task", "task", name, "error", err)
	}
}

func (r *Runner) Load() {
	conf := r.conf.Press
	if !conf.Open {
		return
	}

	batch := xgo.RandInt(conf.Batch[0], conf.Batch[1])
	toLoad := min(conf.Num-r.count.Load(), batch)
	if toLoad <= 0 {
		return
	}

	startID := conf.StartID
	idRange := int64(conf.Num) * 2
	loaded := int32(0)
	attempts := int32(0)

	for loaded < toLoad && attempts < conf.Num {
		attempts++

		id := startID + ((r.nextID.Add(1) - 1) % idRange)

		if _, exists := r.users.Load(id); exists {
			continue
		}

		user, err := NewUser(id, r)
		if err != nil || user == nil {
			slog.Warn("load press user", "uid", id, "error", err)
			continue
		}

		r.users.Store(id, user)
		r.count.Add(1)
		loaded++
	}
}

func (r *Runner) Release() {
	r.users.Range(func(key, value interface{}) bool {
		user := value.(*User)
		if !user.IsFree() {
			return true
		}
		user.Release()
		r.users.Delete(key)
		r.count.Add(-1)
		return true
	})
}
