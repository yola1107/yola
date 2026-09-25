package mailbox

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

func newTestGroup(t *testing.T, mailboxCount, workerCount, queueSize, batchSize int) *Group {
	t.Helper()
	group, err := NewGroup(mailboxCount, workerCount, queueSize, batchSize)
	if err != nil {
		t.Fatal(err)
	}
	if err = group.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := group.Stop(context.Background()); err != nil {
			t.Errorf("stop mailbox group: %v", err)
		}
	})
	return group
}

func TestGroupSerializesOneMailboxAndRunsDifferentMailboxesConcurrently(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		group := newTestGroup(t, 2, 2, 8, 1)
		firstMailbox := group.Executor(0)
		secondMailbox := group.Executor(1)
		firstStarted := make(chan struct{})
		releaseFirst := make(chan struct{})
		release := sync.OnceFunc(func() { close(releaseFirst) })
		t.Cleanup(release)
		otherMailboxRan := make(chan struct{})
		secondSameMailboxRan := make(chan struct{})

		if err := firstMailbox.TryPost(func() { close(firstStarted); <-releaseFirst }); err != nil {
			t.Fatal(err)
		}
		<-firstStarted
		if err := firstMailbox.TryPost(func() { close(secondSameMailboxRan) }); err != nil {
			t.Fatal(err)
		}
		if err := secondMailbox.TryPost(func() { close(otherMailboxRan) }); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		select {
		case <-otherMailboxRan:
		default:
			t.Fatal("different mailbox did not run concurrently")
		}
		select {
		case <-secondSameMailboxRan:
			t.Fatal("same mailbox ran concurrently")
		default:
		}
		release()
		synctest.Wait()
		select {
		case <-secondSameMailboxRan:
		default:
			t.Fatal("same mailbox did not preserve progress")
		}
	})
}

func TestGroupStatsTracksRunningAcrossBatches(t *testing.T) {
	group := newTestGroup(t, 1, 2, 1, 1)
	executor := group.Executor(0)
	var reportedIdle atomic.Int32
	for range 10000 {
		if err := group.Post(t.Context(), 0, func() {
			// 让交接批次的前一个 worker 完成收尾，再观察当前任务的状态。
			runtime.Gosched()
			if executor.Stats().Running != 1 {
				reportedIdle.Add(1)
			}
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := group.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if count := reportedIdle.Load(); count != 0 {
		t.Errorf("Stats reported idle during %d running jobs", count)
	}
	if stats := executor.Stats(); stats != (Stats{Capacity: 1, Free: 1}) {
		t.Errorf("Stats after Stop = %+v, want an idle empty mailbox", stats)
	}
}

func TestGroupAppliesPerMailboxBackpressure(t *testing.T) {
	group := newTestGroup(t, 1, 1, 1, 1)
	mailbox := group.Executor(0)

	started := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	if err := mailbox.TryPost(func() { close(started); <-release }); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := mailbox.TryPost(func() {}); err != nil {
		t.Fatal(err)
	}
	if err := mailbox.TryPost(func() {}); err != ErrFull {
		t.Fatalf("third TryPost() error = %v, want %v", err, ErrFull)
	}
}

func TestCallsCancelWorkBeforeItStarts(t *testing.T) {
	for _, name := range []string{"Call", "PostAndWait"} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				group := newTestGroup(t, 1, 1, 1, 1)
				call := group.Executor(0).Call
				if name == "PostAndWait" {
					call = func(ctx context.Context, job func() error) error { return group.PostAndWait(ctx, 0, job) }
				}
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				if err := call(ctx, func() error { return nil }); err != context.Canceled {
					t.Fatalf("pre-canceled call error = %v, want %v", err, context.Canceled)
				}

				started := make(chan struct{})
				release := make(chan struct{})
				releaseJob := sync.OnceFunc(func() { close(release) })
				t.Cleanup(releaseJob)
				if err := group.Executor(0).TryPost(func() { close(started); <-release }); err != nil {
					t.Fatal(err)
				}
				<-started
				ctx, cancel = context.WithCancel(t.Context())
				defer cancel()
				actionRan := make(chan struct{})
				result := make(chan error, 1)
				go func() {
					result <- call(ctx, func() error { close(actionRan); return nil })
				}()
				synctest.Wait()
				if group.Executor(0).Stats().Free != 0 {
					t.Fatal("call was not queued")
				}
				cancel()
				if err := <-result; !errors.Is(err, context.Canceled) {
					t.Fatalf("queued call error = %v, want %v", err, context.Canceled)
				}
				releaseJob()
				if err := group.Stop(t.Context()); err != nil {
					t.Fatal(err)
				}
				select {
				case <-actionRan:
					t.Fatal("canceled queued action ran")
				default:
				}
			})
		})
	}
}

func TestExecutorCallWaitsForStartedJobAfterCancellation(t *testing.T) {
	for _, name := range []string{"Call", "PostAndWait"} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				group := newTestGroup(t, 1, 1, 1, 1)
				call := group.Executor(0).Call
				if name == "PostAndWait" {
					call = func(ctx context.Context, job func() error) error { return group.PostAndWait(ctx, 0, job) }
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				started := make(chan struct{})
				release := make(chan struct{})
				releaseJob := sync.OnceFunc(func() { close(release) })
				t.Cleanup(releaseJob)
				result := make(chan error, 1)
				go func() {
					result <- call(ctx, func() error {
						close(started)
						<-release
						return nil
					})
				}()
				<-started
				cancel()
				synctest.Wait()
				select {
				case err := <-result:
					t.Fatalf("started call returned before completion: %v", err)
				default:
				}
				releaseJob()
				if err := <-result; err != nil {
					t.Fatalf("started call error = %v, want nil", err)
				}
			})
		})
	}
}

func TestExecutorCallReportsPanicAndStopped(t *testing.T) {
	group := newTestGroup(t, 1, 1, 1, 1)
	mailbox := group.Executor(0)
	if err := mailbox.Call(t.Context(), func() error {
		panic("boom")
	}); err == nil {
		t.Fatal("Call() error = nil after panic")
	}
	if err := group.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := mailbox.Call(t.Context(), func() error { return nil }); !errors.Is(err, ErrStopped) {
		t.Fatalf("Call() after Stop = %v, want %v", err, ErrStopped)
	}
}

func TestGroupStopHonorsContextAndFinishesAfterActiveJob(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		group := newTestGroup(t, 1, 1, 1, 1)
		started := make(chan struct{})
		release := make(chan struct{})
		releaseJob := sync.OnceFunc(func() { close(release) })
		t.Cleanup(releaseJob)
		mailbox := group.Executor(0)
		if err := mailbox.TryPost(func() { close(started); <-release }); err != nil {
			t.Fatal(err)
		}
		<-started
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
		defer cancel()
		if err := group.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Stop error = %v, want %v", err, context.DeadlineExceeded)
		}
		if err := mailbox.TryPost(func() {}); !errors.Is(err, ErrStopped) {
			t.Fatalf("TryPost after Stop error = %v, want %v", err, ErrStopped)
		}

		finished := make(chan error, 1)
		go func() { finished <- group.Stop(t.Context()) }()
		synctest.Wait()
		select {
		case err := <-finished:
			t.Fatalf("second Stop returned before active job completed: %v", err)
		default:
		}
		releaseJob()
		if err := <-finished; err != nil {
			t.Fatalf("second Stop error = %v", err)
		}
		if err := group.Stop(t.Context()); err != nil {
			t.Fatalf("completed Stop error = %v", err)
		}
	})
}

func TestGroupPostWaitsForCapacityOrStop(t *testing.T) {
	t.Run("accepted", func(t *testing.T) {
		group := newTestGroup(t, 1, 1, 1, 1)

		started := make(chan struct{})
		release := make(chan struct{})
		releaseJob := sync.OnceFunc(func() { close(release) })
		t.Cleanup(releaseJob)
		controlRan := make(chan struct{})
		mailbox := group.Executor(0)
		if err := mailbox.TryPost(func() { close(started); <-release }); err != nil {
			t.Fatal(err)
		}
		<-started
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if err := group.Post(ctx, 0, func() { close(controlRan) }); err != nil {
			t.Fatal(err)
		}
		cancel()
		releaseJob()
		if err := group.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
		select {
		case <-controlRan:
		default:
			t.Fatal("accepted control event was canceled before execution")
		}
	})

	t.Run("capacity", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			group := newTestGroup(t, 1, 1, 1, 1)
			started := make(chan struct{})
			release := make(chan struct{})
			releaseJob := sync.OnceFunc(func() { close(release) })
			t.Cleanup(releaseJob)
			controlRan := make(chan struct{})
			mailbox := group.Executor(0)
			if err := mailbox.TryPost(func() { close(started); <-release }); err != nil {
				t.Fatal(err)
			}
			<-started
			if err := mailbox.TryPost(func() {}); err != nil {
				t.Fatal(err)
			}
			accepted := make(chan error, 1)
			go func() { accepted <- group.Post(t.Context(), 0, func() { close(controlRan) }) }()
			synctest.Wait()
			select {
			case err := <-accepted:
				t.Fatalf("Post returned while queue was full: %v", err)
			default:
			}
			releaseJob()
			if err := <-accepted; err != nil {
				t.Fatalf("Post error = %v", err)
			}
			<-controlRan
		})
	})

	t.Run("stop", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			group := newTestGroup(t, 1, 1, 1, 1)
			started := make(chan struct{})
			release := make(chan struct{})
			releaseJob := sync.OnceFunc(func() { close(release) })
			t.Cleanup(releaseJob)
			mailbox := group.Executor(0)
			if err := mailbox.TryPost(func() { close(started); <-release }); err != nil {
				t.Fatal(err)
			}
			<-started
			if err := mailbox.TryPost(func() {}); err != nil {
				t.Fatal(err)
			}
			waitResult := make(chan error, 1)
			go func() { waitResult <- group.Post(t.Context(), 0, func() {}) }()
			synctest.Wait()
			stopDone := make(chan error, 1)
			go func() { stopDone <- group.Stop(t.Context()) }()
			if err := <-waitResult; !errors.Is(err, ErrStopped) {
				t.Fatalf("Post stop error = %v, want %v", err, ErrStopped)
			}
			releaseJob()
			if err := <-stopDone; err != nil {
				t.Fatalf("Stop error = %v", err)
			}
		})
	})
}

func TestMiddlewarePreservesContextAndHandlerError(t *testing.T) {
	group := newTestGroup(t, 1, 1, 8, 1)
	executor := &callCountingExecutor{Executor: group.Executor(0)}

	type contextKey struct{}
	handler := Middleware(executor)(func(ctx context.Context, _ any) (any, error) {
		if ctx.Value(contextKey{}) != "player-a" {
			return nil, errors.New("context value is missing")
		}
		return new(emptypb.Empty), nil
	})
	ctx := context.WithValue(context.Background(), contextKey{}, "player-a")
	if _, err := handler(ctx, new(emptypb.Empty)); err != nil {
		t.Fatal(err)
	}

	wantErr := errors.New("handler failed")
	failingHandler := Middleware(executor)(func(context.Context, any) (any, error) {
		return nil, wantErr
	})
	if _, err := failingHandler(context.Background(), new(emptypb.Empty)); !errors.Is(err, wantErr) {
		t.Fatalf("handler error = %v, want %v", err, wantErr)
	}
	if calls := executor.calls.Load(); calls != 2 {
		t.Fatalf("executor calls = %d, want 2", calls)
	}
}

func TestMiddlewareMapsExecutorErrors(t *testing.T) {
	tests := []struct {
		err  error
		code codes.Code
	}{
		{ErrFull, codes.ResourceExhausted},
		{ErrNotStarted, codes.Unavailable},
		{ErrStopped, codes.Unavailable},
	}
	for _, test := range tests {
		if got := status.Code(MapError(test.err)); got != test.code {
			t.Fatalf("MapError(%v) code = %v, want %v", test.err, got, test.code)
		}
	}
	for _, err := range []error{context.Canceled, context.DeadlineExceeded} {
		if !errors.Is(MapError(err), err) {
			t.Fatalf("MapError(%v) did not preserve context error", err)
		}
	}
}

type callCountingExecutor struct {
	Executor
	calls atomic.Int32
}

func (executor *callCountingExecutor) Call(ctx context.Context, job func() error) error {
	executor.calls.Add(1)
	return executor.Executor.Call(ctx, job)
}
