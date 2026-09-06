package press

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"yola/test/internal/ants"
	"yola/test/internal/xgo"
	"yola/test/ludo/api/v1"
	"yola/test/ludo/internal/pressure"
)

type Runner struct {
	conf Press

	workers *ants.Pool
	ctx     context.Context
	cancel  context.CancelFunc
	stop    sync.Once
	jobs    sync.WaitGroup

	actionSlots chan struct{}
	connectUser func(*User) error
	loginToken  string

	usersMu      sync.RWMutex
	users        map[int64]*User
	nextID       atomic.Int64
	uidExhausted atomic.Bool

	closedPlayers   atomic.Int64
	startupRejected atomic.Int64
	stages          [startupStageCount]stageMetric
	commands        [int(v1.GameCommand_CmdMove) + 1]atomic.Int64
	broadcasts      broadcastMetric
}

type userCounts struct {
	states        [userStateCount]int32
	authenticated int32
	seated        int32
	ready         int32
	playing       int32
}

func NewRunner(conf Press) *Runner {
	conf = withPressDefaults(conf)
	loginToken, err := pressure.Token(pressure.MoneyRange{Min: conf.MinMoney, Max: conf.MaxMoney})
	if err != nil {
		panic(fmt.Sprintf("create pressure login token: %v", err))
	}
	ctx, cancel := context.WithCancel(context.Background())
	workers, err := ants.New(
		ants.WithSize(conf.Concurrency),
		ants.WithNonblocking(true),
	)
	if err != nil {
		panic(fmt.Sprintf("create pressure workers: %v", err))
	}
	runner := &Runner{
		conf:        conf,
		workers:     workers,
		ctx:         ctx,
		cancel:      cancel,
		actionSlots: make(chan struct{}, conf.ActionConcurrency),
		users:       make(map[int64]*User, max(int(conf.Num), 0)),
		loginToken:  loginToken,
	}
	runner.connectUser = func(user *User) error { return user.Init() }
	return runner
}

func (r *Runner) runAction(job func()) error {
	if job == nil {
		return errors.New("pressure action is nil")
	}
	if err := r.ctx.Err(); err != nil {
		return err
	}
	select {
	case r.actionSlots <- struct{}{}:
	case <-r.ctx.Done():
		return r.ctx.Err()
	}
	defer func() { <-r.actionSlots }()
	job()
	return nil
}

func (r *Runner) Start() error {
	if r.conf.Interval <= 0 {
		return errors.New("start pressure client: interval must be positive")
	}
	if err := r.workers.Start(r.ctx); err != nil {
		return fmt.Errorf("start pressure workers: %w", err)
	}
	if r.conf.Open {
		probe := &User{runner: r, id: r.conf.StartID}
		client, err := probe.connect()
		if err != nil {
			r.Stop()
			return fmt.Errorf("authenticate pressure client: %w", err)
		}
		client.Close()
	}
	r.jobs.Add(1)
	go r.maintain()
	slog.Info("start pressure client",
		"scenario", r.conf.Scenario,
		"target_players", r.conf.Num,
		"uid_start", r.conf.StartID,
		"uid_end", r.conf.StartID+r.conf.UIDCount-1,
	)
	return nil
}

func (r *Runner) maintain() {
	defer r.jobs.Done()
	load := time.NewTicker(time.Duration(r.conf.Interval) * time.Millisecond)
	monitor := time.NewTicker(15 * time.Second)
	defer load.Stop()
	defer monitor.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-load.C:
			r.Load()
		case <-monitor.C:
			r.maintainUsers()
		}
	}
}

func (r *Runner) Stop() {
	r.stop.Do(func() {
		r.cancel()
		r.jobs.Wait()
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := r.workers.Stop(stopCtx); err != nil {
			slog.Warn("stop pressure workers", "error", err)
		}
		cancel()
		for _, user := range r.userSnapshot() {
			r.RemoveUser(user)
		}
		slog.Info("stop pressure client")
	})
}

func (r *Runner) monitor(counts userCounts) {
	slog.Info("pressure client status",
		"workers", r.workers.Monitor(),
		"action_capacity", cap(r.actionSlots),
		"action_inflight", len(r.actionSlots),
		"target_players", r.conf.Num,
		"tracked_players", r.userCount(),
		"starting_players", counts.states[userStarting],
		"active_players", counts.states[userActive],
		"closing_players", counts.states[userClosing],
		"authenticated", counts.authenticated,
		"seated", counts.seated,
		"ready", counts.ready,
		"playing", counts.playing,
		"closed_total", r.closedPlayers.Load(),
		"startup_rejected", r.startupRejected.Load(),
		"stages", r.stageReports(),
		"commands", r.commandCounts(),
		"broadcasts", r.broadcasts.report(),
	)
}

func (r *Runner) maintainUsers() {
	var counts userCounts
	var free []*User
	r.usersMu.RLock()
	for _, user := range r.users {
		if user.IsFree() {
			free = append(free, user)
			continue
		}
		state := user.stateValue()
		if state >= 0 && state < userStateCount {
			counts.states[state]++
		}
		if user.authenticated.Load() {
			counts.authenticated++
		}
		if user.seated.Load() {
			counts.seated++
		}
		if user.ready.Load() {
			counts.ready++
		}
		if user.playing.Load() {
			counts.playing++
		}
	}
	r.usersMu.RUnlock()

	for _, user := range free {
		r.RemoveUser(user)
	}
	r.monitor(counts)
}

func (r *Runner) Load() {
	if !r.conf.Open {
		return
	}
	batch := xgo.RandInt(r.conf.Batch[0], r.conf.Batch[1])
	toLoad := min(r.conf.Num-int32(r.userCount()), batch)
	if toLoad <= 0 {
		return
	}

	loaded := int32(0)
	for loaded < toLoad {
		id, ok := r.nextUserID()
		if !ok {
			if r.uidExhausted.CompareAndSwap(false, true) {
				slog.Error("pressure UID range exhausted",
					"start_id", r.conf.StartID,
					"uid_count", r.conf.UIDCount,
				)
			}
			return
		}
		user := newUser(id, r)
		if !r.trackUser(user) {
			continue
		}
		if err := r.workers.Submit(func() { r.startUser(user) }); err != nil {
			if r.ctx.Err() == nil {
				r.startupRejected.Add(1)
				if !errors.Is(err, ants.ErrFull) {
					slog.Warn("start pressure user", "uid", id, "error", err)
				}
			}
			r.RemoveUser(user)
			if errors.Is(err, ants.ErrFull) {
				break
			}
			return
		}
		loaded++
	}
}

func (r *Runner) startUser(user *User) {
	started := time.Now()
	connectErr := r.connectUser(user)
	r.recordStage(stageConnect, started, connectErr)
	if connectErr != nil {
		if r.ctx.Err() == nil {
			slog.Error("connect pressure user", "uid", user.id, "error", connectErr)
		}
		r.RemoveUser(user)
		return
	}

	user.eventMu.Lock()
	if user.stateValue() != userStarting {
		user.eventMu.Unlock()
		user.Release()
		return
	}
	user.authenticated.Store(true)
	if r.conf.Scenario == scenarioConnect {
		r.transitionUser(user, userStarting, userActive)
		user.eventMu.Unlock()
		return
	}
	started = time.Now()
	startupErr := user.login()
	r.recordStage(stageLogin, started, startupErr)
	failedStage := stageLogin
	status := int32(0)
	if startupErr == nil {
		started = time.Now()
		status, startupErr = user.scene()
		r.recordStage(stageScene, started, startupErr)
		failedStage = stageScene
	}
	if startupErr == nil {
		started = time.Now()
		startupErr = user.markReady(status)
		r.recordStage(stageReady, started, startupErr)
		failedStage = stageReady
	}
	if startupErr == nil {
		r.transitionUser(user, userStarting, userActive)
	}
	user.eventMu.Unlock()

	if startupErr != nil {
		if r.ctx.Err() == nil {
			slog.Error("prepare pressure user",
				"uid", user.id,
				"stage", startupStageNames[failedStage],
				"error", startupErr,
			)
		}
		r.RemoveUser(user)
	}
}

func (r *Runner) RemoveUser(user *User) {
	if user == nil {
		return
	}
	user.eventMu.Lock()
	for {
		state := user.stateValue()
		if state == userClosing || state == userClosed {
			user.eventMu.Unlock()
			return
		}
		if r.transitionUser(user, state, userClosing) {
			break
		}
	}
	user.logout.Store(true)
	user.eventMu.Unlock()

	user.Release()
	r.usersMu.Lock()
	tracked := r.users[user.id] == user
	if tracked {
		delete(r.users, user.id)
	}
	r.usersMu.Unlock()
	if r.transitionUser(user, userClosing, userClosed) && tracked {
		r.closedPlayers.Add(1)
	}
}

func (r *Runner) trackUser(user *User) bool {
	if user == nil || user.stateValue() != userStarting {
		return false
	}
	r.usersMu.Lock()
	defer r.usersMu.Unlock()
	if _, exists := r.users[user.id]; exists {
		return false
	}
	r.users[user.id] = user
	return true
}

func (r *Runner) userCount() int {
	r.usersMu.RLock()
	defer r.usersMu.RUnlock()
	return len(r.users)
}

func (r *Runner) userSnapshot() []*User {
	r.usersMu.RLock()
	defer r.usersMu.RUnlock()
	users := make([]*User, 0, len(r.users))
	for _, user := range r.users {
		users = append(users, user)
	}
	return users
}

func (r *Runner) transitionUser(user *User, from, to userState) bool {
	return user != nil && user.state.CompareAndSwap(int32(from), int32(to))
}

func (r *Runner) nextUserID() (int64, bool) {
	offset := r.nextID.Add(1) - 1
	if offset >= r.conf.UIDCount {
		return 0, false
	}
	return r.conf.StartID + offset, true
}

func (r *Runner) recordStage(stage startupStage, started time.Time, err error) {
	if err != nil && r.ctx.Err() != nil {
		return
	}
	r.stages[stage].record(time.Since(started), err == nil)
}

func (r *Runner) stageReports() map[string]stageReport {
	reports := make(map[string]stageReport, startupStageCount)
	for stage, name := range startupStageNames {
		reports[name] = r.stages[stage].report()
	}
	return reports
}

func (r *Runner) recordCommand(command v1.GameCommand) {
	if command >= v1.GameCommand_CmdLogin && command <= v1.GameCommand_CmdMove {
		r.commands[int(command)].Add(1)
	}
}

func (r *Runner) commandCounts() map[string]int64 {
	counts := make(map[string]int64, int(v1.GameCommand_CmdMove))
	for command := v1.GameCommand_CmdLogin; command <= v1.GameCommand_CmdMove; command++ {
		counts[command.String()] = r.commands[int(command)].Load()
	}
	return counts
}
