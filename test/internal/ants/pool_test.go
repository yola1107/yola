package ants

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func BenchmarkPoolSubmit(b *testing.B) {
	pool, err := New(WithSize(128), WithNonblocking(false))
	if err != nil {
		b.Fatal(err)
	}
	if err = pool.Start(context.Background()); err != nil {
		b.Fatal(err)
	}
	defer func() {
		if stopErr := pool.Stop(context.Background()); stopErr != nil {
			b.Error(stopErr)
		}
	}()

	var tasks sync.WaitGroup
	tasks.Add(b.N)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err = pool.Submit(tasks.Done); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	tasks.Wait()
}

func TestPoolLifecycle(t *testing.T) {
	pool, err := New(WithSize(1))
	if err != nil {
		t.Fatal(err)
	}
	if err = pool.Submit(func() {}); !errors.Is(err, ErrNotStarted) {
		t.Fatalf("submit before Start error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err = pool.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err = pool.Start(ctx); !errors.Is(err, ErrStarted) {
		t.Fatalf("second Start error = %v", err)
	}

	done := make(chan struct{})
	if err = pool.Submit(func() { close(done) }); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("submitted task did not run")
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	if err = pool.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	if err = pool.Submit(func() {}); !errors.Is(err, ErrClosed) {
		t.Fatalf("submit after Stop error = %v", err)
	}
	if err = pool.Stop(stopCtx); err != nil {
		t.Fatalf("second Stop error = %v", err)
	}
	canceledCtx, cancelCanceled := context.WithCancel(context.Background())
	cancelCanceled()
	if err = pool.Stop(canceledCtx); err != nil {
		t.Fatalf("Stop completed pool with canceled context error = %v", err)
	}
}

func TestPoolStopBeforeStart(t *testing.T) {
	pool, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if monitor := pool.Monitor(); monitor != (Monitor{}) {
		t.Fatalf("Monitor before Start = %+v", monitor)
	}
	if err = pool.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = pool.Start(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("Start after Stop error = %v", err)
	}
}

func TestPoolStopHonorsContext(t *testing.T) {
	pool, err := New(WithSize(1), WithNonblocking(false))
	if err != nil {
		t.Fatal(err)
	}
	if err = pool.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	if err = pool.Submit(func() {
		close(started)
		<-release
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("blocking task did not start")
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	err = pool.Stop(stopCtx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		close(release)
		t.Fatalf("Stop error = %v", err)
	}
	close(release)
	if err = pool.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPoolRejectsOverload(t *testing.T) {
	pool, err := New(WithSize(1), WithNonblocking(true))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err = pool.Start(ctx); err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	if err = pool.Submit(func() {
		close(started)
		<-release
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("blocking task did not start")
	}
	if err = pool.Submit(func() {}); !errors.Is(err, ErrFull) {
		t.Fatalf("overload error = %v", err)
	}
	close(release)

	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	if err = pool.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
}

func TestPoolContextCancellationStopsPool(t *testing.T) {
	pool, err := New()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err = pool.Start(ctx); err != nil {
		t.Fatal(err)
	}
	cancel()

	select {
	case <-pool.done:
	case <-time.After(time.Second):
		t.Fatal("pool did not stop after context cancellation")
	}
	if err = pool.Submit(func() {}); !errors.Is(err, ErrClosed) {
		t.Fatalf("submit after context cancellation error = %v, want %v", err, ErrClosed)
	}
}

func TestPoolRecoversTaskPanic(t *testing.T) {
	pool, err := New(WithSize(1), WithNonblocking(false))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err = pool.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err = pool.Submit(func() { panic("boom") }); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	if err = pool.Submit(func() { close(done) }); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not continue after panic")
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	if err = pool.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
}

func TestPoolLogsTaskPanicWithNilLogger(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	pool, err := New(WithSize(1), WithNonblocking(false), WithLogger(nil))
	if err != nil {
		t.Fatal(err)
	}
	if err = pool.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if stopErr := pool.Stop(context.Background()); stopErr != nil {
			t.Error(stopErr)
		}
	}()

	if err = pool.Submit(func() { panic("nil logger panic") }); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	if err = pool.Submit(func() { close(done) }); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not continue after panic")
	}
	logged := output.String()
	if !strings.Contains(logged, "worker pool task panic") || !strings.Contains(logged, "nil logger panic") {
		t.Fatalf("panic log = %q", logged)
	}
}

func TestPoolConcurrentStopSubmitCompletesSuccessfulTasks(t *testing.T) {
	pool, err := New(WithSize(4), WithNonblocking(true))
	if err != nil {
		t.Fatal(err)
	}
	if err = pool.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	var accepted atomic.Int64
	var completed atomic.Int64
	release := make(chan struct{})
	started := make(chan struct{})
	if err = pool.Submit(func() {
		close(started)
		<-release
		completed.Add(1)
	}); err != nil {
		t.Fatal(err)
	}
	accepted.Add(1)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("initial task did not start")
	}

	const submitters = 128
	start := make(chan struct{})
	results := make(chan error, submitters)
	var submissions sync.WaitGroup
	submissions.Add(submitters)
	for range submitters {
		go func() {
			defer submissions.Done()
			<-start
			submitErr := pool.Submit(func() {
				<-release
				completed.Add(1)
			})
			if submitErr == nil {
				accepted.Add(1)
			}
			results <- submitErr
		}()
	}

	stopDone := make(chan error, 1)
	close(start)
	go func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		stopDone <- pool.Stop(stopCtx)
	}()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		pool.mu.Lock()
		stopping := pool.state != stateRunning
		pool.mu.Unlock()
		if stopping {
			break
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case stopErr := <-stopDone:
		close(release)
		t.Fatalf("Stop returned before successful tasks completed: %v", stopErr)
	default:
	}
	close(release)
	submissions.Wait()
	close(results)
	for submitErr := range results {
		if submitErr != nil && !errors.Is(submitErr, ErrClosed) && !errors.Is(submitErr, ErrFull) {
			t.Fatalf("Submit during Stop error = %v", submitErr)
		}
	}
	if err = <-stopDone; err != nil {
		t.Fatal(err)
	}
	if got, want := completed.Load(), accepted.Load(); got != want {
		t.Fatalf("completed successful tasks = %d, want %d", got, want)
	}
}

func TestPoolRejectsInvalidConfiguration(t *testing.T) {
	if _, err := New(WithSize(0)); err == nil {
		t.Fatal("New accepted zero size")
	}
	if _, err := New(WithExpiryTime(0)); err == nil {
		t.Fatal("New accepted zero expiry")
	}
	if _, err := New(WithMaxBlockingTasks(-1)); err == nil {
		t.Fatal("New accepted negative max blocking tasks")
	}
}
