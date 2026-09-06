package timer_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"yola/test/internal/ants"
	"yola/test/internal/timer"
	timerheap "yola/test/internal/timer/heap"
	"yola/test/internal/timer/stdlib"
	"yola/test/internal/timer/wheel"
)

type schedulerConstructor func(*testing.T) timer.Scheduler

func TestSchedulers(t *testing.T) {
	constructors := map[string]schedulerConstructor{
		"stdlib": func(*testing.T) timer.Scheduler { return stdlib.New() },
		"heap": func(t *testing.T) timer.Scheduler {
			t.Helper()
			scheduler, err := timerheap.New()
			if err != nil {
				t.Fatal(err)
			}
			return scheduler
		},
		"wheel": func(t *testing.T) timer.Scheduler {
			t.Helper()
			scheduler, err := wheel.New(wheel.WithTick(time.Millisecond), wheel.WithWheelSize(16))
			if err != nil {
				t.Fatal(err)
			}
			return scheduler
		},
	}

	for name, newScheduler := range constructors {
		t.Run(name, func(t *testing.T) {
			t.Run("lifecycle", func(t *testing.T) { testSchedulerLifecycle(t, newScheduler) })
			t.Run("once", func(t *testing.T) { testSchedulerOnce(t, newScheduler) })
			t.Run("panic recovery", func(t *testing.T) { testSchedulerPanic(t, newScheduler) })
			t.Run("periodic panic recovery", func(t *testing.T) { testSchedulerPeriodicPanic(t, newScheduler) })
			t.Run("cancel", func(t *testing.T) { testSchedulerCancel(t, newScheduler) })
			t.Run("cancel all", func(t *testing.T) { testSchedulerCancelAll(t, newScheduler) })
			t.Run("forever modes", func(t *testing.T) { testSchedulerForeverModes(t, newScheduler) })
			t.Run("periodic task does not overlap", func(t *testing.T) { testSchedulerPeriodic(t, newScheduler) })
			t.Run("stop honors context", func(t *testing.T) { testSchedulerStop(t, newScheduler) })
			t.Run("validation", func(t *testing.T) { testSchedulerValidation(t, newScheduler) })
		})
	}
}

func testSchedulerLifecycle(t *testing.T, newScheduler schedulerConstructor) {
	scheduler := newScheduler(t)
	var nilContext context.Context // Context 零值为 nil，用于验证显式拒绝逻辑。
	if err := scheduler.Start(nilContext); err == nil {
		t.Fatal("Start accepted nil context")
	}
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := scheduler.Start(canceledCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start canceled context error = %v", err)
	}
	startScheduler(t, scheduler)
	if err := scheduler.Start(context.Background()); !errors.Is(err, timer.ErrStarted) {
		t.Fatalf("second Start error = %v", err)
	}
	if err := scheduler.Stop(nilContext); err == nil {
		t.Fatal("Stop accepted nil context")
	}
	stopScheduler(t, scheduler)
}

func testSchedulerOnce(t *testing.T, newScheduler schedulerConstructor) {
	scheduler := newScheduler(t)
	executed := make(chan struct{})
	id, err := scheduler.Once(0, func() { close(executed) })
	if err != nil {
		t.Fatal(err)
	}
	if id == 0 || scheduler.Monitor().Total != 1 {
		t.Fatalf("id = %d, monitor = %+v", id, scheduler.Monitor())
	}
	startScheduler(t, scheduler)
	waitClosed(t, executed, time.Second, "once task did not execute")
	waitFor(t, time.Second, func() bool { return scheduler.Monitor().Total == 0 })
	stopScheduler(t, scheduler)
}

func testSchedulerPanic(t *testing.T, newScheduler schedulerConstructor) {
	scheduler := newScheduler(t)
	panicked := make(chan struct{})
	continued := make(chan struct{})
	if _, err := scheduler.Once(0, func() {
		close(panicked)
		panic("boom")
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := scheduler.Once(5*time.Millisecond, func() { close(continued) }); err != nil {
		t.Fatal(err)
	}
	startScheduler(t, scheduler)
	waitClosed(t, panicked, time.Second, "panicking callback did not run")
	waitClosed(t, continued, time.Second, "scheduler stopped after callback panic")
	stopScheduler(t, scheduler)
}

func testSchedulerPeriodicPanic(t *testing.T, newScheduler schedulerConstructor) {
	scheduler := newScheduler(t)
	var calls atomic.Int32
	continued := make(chan struct{})
	id, err := scheduler.ForeverNow(5*time.Millisecond, func() {
		switch calls.Add(1) {
		case 1:
			panic("periodic boom")
		case 2:
			close(continued)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	startScheduler(t, scheduler)
	waitClosed(t, continued, time.Second, "periodic task stopped after callback panic")
	if !scheduler.Cancel(id) {
		t.Fatal("Cancel rejected periodic task after panic recovery")
	}
	stopScheduler(t, scheduler)
}

func testSchedulerCancel(t *testing.T, newScheduler schedulerConstructor) {
	scheduler := newScheduler(t)
	var executed atomic.Bool
	id, err := scheduler.Once(20*time.Millisecond, func() { executed.Store(true) })
	if err != nil {
		t.Fatal(err)
	}
	if !scheduler.Cancel(id) || scheduler.Cancel(id) {
		t.Fatal("Cancel result is inconsistent")
	}
	startScheduler(t, scheduler)
	time.Sleep(40 * time.Millisecond)
	if executed.Load() {
		t.Fatal("canceled task executed")
	}
	stopScheduler(t, scheduler)
}

func testSchedulerCancelAll(t *testing.T, newScheduler schedulerConstructor) {
	scheduler := newScheduler(t)
	firstID, err := scheduler.Once(time.Hour, func() {})
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := scheduler.Forever(time.Hour, func() {})
	if err != nil {
		t.Fatal(err)
	}
	if firstID == secondID || scheduler.Monitor().Total != 2 {
		t.Fatalf("task IDs = (%d, %d), monitor = %+v", firstID, secondID, scheduler.Monitor())
	}
	scheduler.CancelAll()
	if scheduler.Monitor().Total != 0 || scheduler.Cancel(firstID) || scheduler.Cancel(secondID) {
		t.Fatalf("tasks remain after CancelAll: monitor = %+v", scheduler.Monitor())
	}
	startScheduler(t, scheduler)
	stopScheduler(t, scheduler)
}

func testSchedulerForeverModes(t *testing.T, newScheduler schedulerConstructor) {
	scheduler := newScheduler(t)
	immediate := make(chan struct{})
	delayed := make(chan struct{})
	immediateID, err := scheduler.ForeverNow(time.Hour, func() { close(immediate) })
	if err != nil {
		t.Fatal(err)
	}
	delayedID, err := scheduler.Forever(40*time.Millisecond, func() { close(delayed) })
	if err != nil {
		t.Fatal(err)
	}
	startScheduler(t, scheduler)
	waitClosed(t, immediate, time.Second, "ForeverNow did not execute immediately")
	select {
	case <-delayed:
		t.Fatal("Forever executed before its first interval")
	case <-time.After(10 * time.Millisecond):
	}
	waitClosed(t, delayed, time.Second, "Forever did not execute after its first interval")
	if !scheduler.Cancel(immediateID) || !scheduler.Cancel(delayedID) {
		t.Fatal("Cancel rejected active periodic task")
	}
	stopScheduler(t, scheduler)
}

func testSchedulerPeriodic(t *testing.T, newScheduler schedulerConstructor) {
	scheduler := newScheduler(t)
	var active, maximum, count atomic.Int32
	twice := make(chan struct{})
	_, err := scheduler.ForeverNow(time.Millisecond, func() {
		current := active.Add(1)
		for {
			old := maximum.Load()
			if current <= old || maximum.CompareAndSwap(old, current) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		active.Add(-1)
		if count.Add(1) == 2 {
			close(twice)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	startScheduler(t, scheduler)
	waitClosed(t, twice, time.Second, "periodic task did not repeat")
	scheduler.CancelAll()
	stopScheduler(t, scheduler)
	if maximum.Load() != 1 {
		t.Fatalf("maximum concurrent callbacks = %d", maximum.Load())
	}
}

func testSchedulerStop(t *testing.T, newScheduler schedulerConstructor) {
	scheduler := newScheduler(t)
	started := make(chan struct{})
	release := make(chan struct{})
	_, err := scheduler.Once(0, func() {
		close(started)
		<-release
	})
	if err != nil {
		t.Fatal(err)
	}
	startScheduler(t, scheduler)
	waitClosed(t, started, time.Second, "blocking task did not start")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	err = scheduler.Stop(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stop error = %v", err)
	}
	close(release)
	stopScheduler(t, scheduler)
}

func testSchedulerValidation(t *testing.T, newScheduler schedulerConstructor) {
	scheduler := newScheduler(t)
	if _, err := scheduler.Once(-time.Second, func() {}); err == nil {
		t.Fatal("Once accepted negative delay")
	}
	if _, err := scheduler.Once(0, nil); err == nil {
		t.Fatal("Once accepted nil callback")
	}
	if _, err := scheduler.Forever(0, func() {}); err == nil {
		t.Fatal("Forever accepted zero interval")
	}
	stopScheduler(t, scheduler)
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := scheduler.Stop(canceledCtx); err != nil {
		t.Fatalf("Stop completed scheduler with canceled context error = %v", err)
	}
	if _, err := scheduler.Once(time.Second, func() {}); !errors.Is(err, timer.ErrStopped) {
		t.Fatalf("Once after Stop error = %v", err)
	}
	if err := scheduler.Start(context.Background()); !errors.Is(err, timer.ErrStopped) {
		t.Fatalf("Start after Stop error = %v", err)
	}
}

func TestSchedulersStopOnContextCancellation(t *testing.T) {
	constructors := []func() (timer.Scheduler, error){
		func() (timer.Scheduler, error) { return stdlib.New(), nil },
		func() (timer.Scheduler, error) { return timerheap.New() },
		func() (timer.Scheduler, error) { return wheel.New(wheel.WithTick(time.Millisecond)) },
	}
	for _, constructor := range constructors {
		scheduler, err := constructor()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		if err = scheduler.Start(ctx); err != nil {
			t.Fatal(err)
		}
		cancel()
		stopScheduler(t, scheduler)
	}
}

func TestConstructorsValidateOptions(t *testing.T) {
	if _, err := timerheap.New(timerheap.WithExecutor(nil)); err == nil {
		t.Fatal("heap.New accepted nil executor")
	}
	for _, test := range []struct {
		name string
		opt  wheel.Option
	}{
		{name: "tick", opt: wheel.WithTick(0)},
		{name: "size", opt: wheel.WithWheelSize(1)},
		{name: "executor", opt: wheel.WithExecutor(nil)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := wheel.New(test.opt); err == nil {
				t.Fatal("wheel.New error = nil")
			}
		})
	}
}

func TestSchedulersRetryRejectedOneShot(t *testing.T) {
	for name, constructor := range executorSchedulerConstructors() {
		t.Run(name, func(t *testing.T) {
			executor := new(rejectOnceExecutor)
			scheduler, err := constructor(executor)
			if err != nil {
				t.Fatal(err)
			}
			executed := make(chan struct{})
			if _, err = scheduler.Once(0, func() { close(executed) }); err != nil {
				t.Fatal(err)
			}
			startScheduler(t, scheduler)
			waitClosed(t, executed, time.Second, "rejected one-shot task was not retried")
			stopScheduler(t, scheduler)
			if calls := executor.calls.Load(); calls < 2 {
				t.Fatalf("executor calls = %d, want at least 2", calls)
			}
		})
	}
}

func TestSchedulersSkipRejectedPeriodicOccurrence(t *testing.T) {
	for name, constructor := range executorSchedulerConstructors() {
		t.Run(name, func(t *testing.T) {
			executor := new(rejectingExecutor)
			scheduler, err := constructor(executor)
			if err != nil {
				t.Fatal(err)
			}
			var executed atomic.Int32
			id, err := scheduler.ForeverNow(10*time.Millisecond, func() { executed.Add(1) })
			if err != nil {
				t.Fatal(err)
			}
			startScheduler(t, scheduler)
			waitFor(t, time.Second, func() bool { return executor.calls.Load() >= 3 })
			if got := executed.Load(); got != 0 {
				t.Fatalf("rejected periodic callback executions = %d, want 0", got)
			}
			if !scheduler.Cancel(id) {
				t.Fatal("Cancel rejected an active periodic task")
			}
			callsAfterCancel := executor.calls.Load()
			time.Sleep(30 * time.Millisecond)
			if calls := executor.calls.Load(); calls != callsAfterCancel {
				t.Fatalf("executor calls after Cancel = %d, want %d", calls, callsAfterCancel)
			}
			stopScheduler(t, scheduler)
		})
	}
}

func TestSchedulersStopRejectedOneShotRetry(t *testing.T) {
	for name, constructor := range executorSchedulerConstructors() {
		t.Run(name, func(t *testing.T) {
			executor := new(rejectingExecutor)
			scheduler, err := constructor(executor)
			if err != nil {
				t.Fatal(err)
			}
			var executed atomic.Bool
			if _, err = scheduler.Once(0, func() { executed.Store(true) }); err != nil {
				t.Fatal(err)
			}
			startScheduler(t, scheduler)
			waitFor(t, time.Second, func() bool { return executor.calls.Load() > 0 })
			stopScheduler(t, scheduler)
			callsAfterStop := executor.calls.Load()
			time.Sleep(30 * time.Millisecond)
			if calls := executor.calls.Load(); calls != callsAfterStop {
				t.Fatalf("executor calls after Stop = %d, want %d", calls, callsAfterStop)
			}
			if executed.Load() {
				t.Fatal("rejected one-shot callback executed")
			}
		})
	}
}

func TestSchedulersCancelSubmittedCallback(t *testing.T) {
	for name, constructor := range executorSchedulerConstructors() {
		t.Run(name, func(t *testing.T) {
			executor := &holdingExecutor{jobs: make(chan func(), 1)}
			scheduler, err := constructor(executor)
			if err != nil {
				t.Fatal(err)
			}
			var executed atomic.Bool
			id, err := scheduler.Once(0, func() { executed.Store(true) })
			if err != nil {
				t.Fatal(err)
			}
			startScheduler(t, scheduler)
			var job func()
			select {
			case job = <-executor.jobs:
			case <-time.After(time.Second):
				t.Fatal("executor did not receive callback")
			}
			if !scheduler.Cancel(id) {
				t.Fatal("Cancel rejected a submitted callback")
			}
			job()
			stopScheduler(t, scheduler)
			if executed.Load() {
				t.Fatal("canceled submitted callback executed")
			}
		})
	}
}

func TestWheelDefaultTickDoesNotFireEarly(t *testing.T) {
	const (
		defaultTick = 500 * time.Millisecond
		delay       = 100 * time.Millisecond
	)
	waitForWallClockPhase(t, defaultTick, 50*time.Millisecond, 100*time.Millisecond)
	scheduler, err := wheel.New()
	if err != nil {
		t.Fatal(err)
	}
	executed := make(chan time.Time, 1)
	registeredAt := time.Now()
	if _, err = scheduler.Once(delay, func() { executed <- time.Now() }); err != nil {
		t.Fatal(err)
	}
	startScheduler(t, scheduler)
	select {
	case executedAt := <-executed:
		if executedAt.Before(registeredAt.Add(delay)) {
			t.Fatalf("callback fired after %s, want at least %s", executedAt.Sub(registeredAt), delay)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("default timing-wheel callback did not execute")
	}
	stopScheduler(t, scheduler)
}

func TestSchedulersUseDefaultLoggerForCallbackPanic(t *testing.T) {
	previous := slog.Default()
	defer slog.SetDefault(previous)

	constructors := map[string]func() (timer.Scheduler, error){
		"stdlib": func() (timer.Scheduler, error) { return stdlib.New(stdlib.WithLogger(nil)), nil },
		"heap":   func() (timer.Scheduler, error) { return timerheap.New(timerheap.WithLogger(nil)) },
		"wheel": func() (timer.Scheduler, error) {
			return wheel.New(wheel.WithTick(time.Millisecond), wheel.WithLogger(nil))
		},
	}
	for name, constructor := range constructors {
		t.Run(name, func(t *testing.T) {
			var output bytes.Buffer
			slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
			scheduler, err := constructor()
			if err != nil {
				t.Fatal(err)
			}
			panicked := make(chan struct{})
			if _, err = scheduler.Once(0, func() {
				defer close(panicked)
				panic("boom")
			}); err != nil {
				t.Fatal(err)
			}
			startScheduler(t, scheduler)
			waitClosed(t, panicked, time.Second, "panicking callback did not run")
			stopScheduler(t, scheduler)
			if !strings.Contains(output.String(), "timer callback panic") {
				t.Fatalf("panic log = %q", output.String())
			}
		})
	}
}

func TestHeapSchedulerWithWorkerPool(t *testing.T) {
	pool, err := ants.New(ants.WithSize(1), ants.WithNonblocking(true))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err = pool.Start(ctx); err != nil {
		t.Fatal(err)
	}
	scheduler, err := timerheap.New(timerheap.WithExecutor(pool))
	if err != nil {
		t.Fatal(err)
	}
	executed := make(chan struct{})
	if _, err = scheduler.Once(0, func() { close(executed) }); err != nil {
		t.Fatal(err)
	}
	startScheduler(t, scheduler)
	waitClosed(t, executed, time.Second, "worker pool did not execute timer callback")
	stopScheduler(t, scheduler)

	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	if err = pool.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
}

type executorSchedulerConstructor func(timer.Executor) (timer.Scheduler, error)

func executorSchedulerConstructors() map[string]executorSchedulerConstructor {
	return map[string]executorSchedulerConstructor{
		"heap": func(executor timer.Executor) (timer.Scheduler, error) {
			return timerheap.New(timerheap.WithExecutor(executor))
		},
		"wheel": func(executor timer.Executor) (timer.Scheduler, error) {
			return wheel.New(wheel.WithTick(time.Millisecond), wheel.WithExecutor(executor))
		},
	}
}

type rejectOnceExecutor struct {
	calls atomic.Int32
}

func (executor *rejectOnceExecutor) Submit(job func()) error {
	if executor.calls.Add(1) == 1 {
		return fmt.Errorf("reject first callback")
	}
	go job()
	return nil
}

type rejectingExecutor struct {
	calls atomic.Int32
}

func (executor *rejectingExecutor) Submit(func()) error {
	executor.calls.Add(1)
	return fmt.Errorf("reject callback")
}

type holdingExecutor struct {
	jobs chan func()
}

func (executor *holdingExecutor) Submit(job func()) error {
	executor.jobs <- job
	return nil
}

func startScheduler(t *testing.T, scheduler timer.Scheduler) {
	t.Helper()
	if err := scheduler.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func stopScheduler(t *testing.T, scheduler timer.Scheduler) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := scheduler.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func waitClosed(t *testing.T, channel <-chan struct{}, timeout time.Duration, message string) {
	t.Helper()
	select {
	case <-channel:
	case <-time.After(timeout):
		t.Fatal(message)
	}
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not met")
}

func waitForWallClockPhase(t *testing.T, tick, minimum, maximum time.Duration) {
	t.Helper()
	deadline := time.Now().Add(2 * tick)
	for time.Now().Before(deadline) {
		phase := time.Duration(time.Now().UnixMilli()%tick.Milliseconds()) * time.Millisecond
		if phase >= minimum && phase <= maximum {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("could not enter timing-wheel test phase")
}
