package mailbox

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

func cleanupGroup(t *testing.T, group *Group) {
	t.Helper()
	t.Cleanup(func() {
		if err := group.Stop(context.Background()); err != nil {
			t.Errorf("stop mailbox group: %v", err)
		}
	})
}

func TestGroupSerializesOneMailboxAndRunsDifferentMailboxesConcurrently(t *testing.T) {
	group, err := NewGroup(2, 2, 8, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = group.Start(); err != nil {
		t.Fatal(err)
	}
	cleanupGroup(t, group)
	firstMailbox := group.Executor(0)
	secondMailbox := group.Executor(1)

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	otherMailboxRan := make(chan struct{})
	secondSameMailboxRan := make(chan struct{})
	var sameMailboxRunning atomic.Int32
	var sameMailboxMaximum atomic.Int32

	if err = firstMailbox.TryPost(func() {
		running := sameMailboxRunning.Add(1)
		sameMailboxMaximum.Store(max(sameMailboxMaximum.Load(), running))
		close(firstStarted)
		<-releaseFirst
		sameMailboxRunning.Add(-1)
	}); err != nil {
		t.Fatal(err)
	}
	<-firstStarted
	if err = firstMailbox.TryPost(func() {
		running := sameMailboxRunning.Add(1)
		sameMailboxMaximum.Store(max(sameMailboxMaximum.Load(), running))
		sameMailboxRunning.Add(-1)
		close(secondSameMailboxRan)
	}); err != nil {
		t.Fatal(err)
	}
	if err = secondMailbox.TryPost(func() { close(otherMailboxRan) }); err != nil {
		t.Fatal(err)
	}

	select {
	case <-otherMailboxRan:
	case <-time.After(time.Second):
		t.Fatal("different mailbox did not run concurrently")
	}
	select {
	case <-secondSameMailboxRan:
		t.Fatal("same mailbox ran concurrently")
	default:
	}
	close(releaseFirst)
	select {
	case <-secondSameMailboxRan:
	case <-time.After(time.Second):
		t.Fatal("same mailbox did not preserve progress")
	}
	if maximum := sameMailboxMaximum.Load(); maximum != 1 {
		t.Fatalf("same mailbox maximum concurrency = %d, want 1", maximum)
	}
}

func TestGroupAppliesPerMailboxBackpressure(t *testing.T) {
	group, err := NewGroup(1, 1, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = group.Start(); err != nil {
		t.Fatal(err)
	}
	cleanupGroup(t, group)
	mailbox := group.Executor(0)

	started := make(chan struct{})
	release := make(chan struct{})
	if err = mailbox.TryPost(func() { close(started); <-release }); err != nil {
		t.Fatal(err)
	}
	<-started
	if err = mailbox.TryPost(func() {}); err != nil {
		t.Fatal(err)
	}
	if err = mailbox.TryPost(func() {}); err != ErrFull {
		t.Fatalf("third TryPost() error = %v, want %v", err, ErrFull)
	}
	close(release)
}

func TestCallsCancelWorkBeforeItStarts(t *testing.T) {
	group, err := NewGroup(1, 1, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = group.Start(); err != nil {
		t.Fatal(err)
	}
	cleanupGroup(t, group)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = group.Executor(0).Call(ctx, func() error { return nil }); err != context.Canceled {
		t.Fatalf("Call() error = %v, want %v", err, context.Canceled)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	if err = group.Executor(0).TryPost(func() { close(started); <-release }); err != nil {
		t.Fatal(err)
	}
	<-started
	ctx, cancel = context.WithCancel(context.Background())
	actionRan := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- group.PostAndWait(ctx, 0, func() error {
			close(actionRan)
			return nil
		})
	}()
	deadline := time.Now().Add(time.Second)
	for group.Executor(0).Stats().Free != 0 {
		if time.Now().After(deadline) {
			t.Fatal("PostAndWait was not queued")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if callErr := <-result; !errors.Is(callErr, context.Canceled) {
		t.Fatalf("Call() error = %v, want %v", callErr, context.Canceled)
	}
	close(release)
	if err = group.PostAndWait(context.Background(), 0, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	select {
	case <-actionRan:
		t.Fatal("canceled queued action ran")
	default:
	}
}

func TestExecutorCallWaitsForStartedJobAfterCancellation(t *testing.T) {
	group, err := NewGroup(1, 1, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = group.Start(); err != nil {
		t.Fatal(err)
	}
	cleanupGroup(t, group)

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	release := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		callErr := group.Executor(0).Call(ctx, func() error {
			close(started)
			<-release
			return nil
		})
		result <- callErr
	}()
	<-started
	cancel()
	select {
	case callErr := <-result:
		t.Fatalf("started call returned before completion: %v", callErr)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if callErr := <-result; callErr != nil {
		t.Fatalf("started call error = %v, want nil", callErr)
	}
}

func TestExecutorCallReportsPanicAndStopped(t *testing.T) {
	group, err := NewGroup(1, 1, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = group.Start(); err != nil {
		t.Fatal(err)
	}
	mailbox := group.Executor(0)
	if err = mailbox.Call(context.Background(), func() error {
		panic("boom")
	}); err == nil {
		t.Fatal("Call() error = nil after panic")
	}
	if err = group.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = mailbox.Call(context.Background(), func() error { return nil }); !errors.Is(err, ErrStopped) {
		t.Fatalf("Call() after Stop = %v, want %v", err, ErrStopped)
	}
}

func TestGroupStopHonorsContextAndFinishesAfterActiveJob(t *testing.T) {
	group, err := NewGroup(1, 1, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = group.Start(); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	mailbox := group.Executor(0)
	if err = mailbox.TryPost(func() {
		close(started)
		<-release
	}); err != nil {
		t.Fatal(err)
	}
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err = group.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stop error = %v, want %v", err, context.DeadlineExceeded)
	}
	if err = mailbox.TryPost(func() {}); !errors.Is(err, ErrStopped) {
		t.Fatalf("TryPost after Stop error = %v, want %v", err, ErrStopped)
	}

	finished := make(chan error, 1)
	go func() { finished <- group.Stop(context.Background()) }()
	select {
	case stopErr := <-finished:
		t.Fatalf("second Stop returned before active job completed: %v", stopErr)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case stopErr := <-finished:
		if stopErr != nil {
			t.Fatalf("second Stop error = %v", stopErr)
		}
	case <-time.After(time.Second):
		t.Fatal("second Stop did not observe eventual completion")
	}
	if err = group.Stop(context.Background()); err != nil {
		t.Fatalf("completed Stop error = %v", err)
	}
}

func TestGroupPostWaitsForCapacityOrStop(t *testing.T) {
	t.Run("accepted", func(t *testing.T) {
		group, err := NewGroup(1, 1, 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		if err = group.Start(); err != nil {
			t.Fatal(err)
		}
		cleanupGroup(t, group)

		started := make(chan struct{})
		release := make(chan struct{})
		controlRan := make(chan struct{})
		mailbox := group.Executor(0)
		if err = mailbox.TryPost(func() { close(started); <-release }); err != nil {
			t.Fatal(err)
		}
		<-started
		ctx, cancel := context.WithCancel(context.Background())
		if err = group.Post(ctx, 0, func() { close(controlRan) }); err != nil {
			t.Fatal(err)
		}
		cancel()
		close(release)
		select {
		case <-controlRan:
		case <-time.After(time.Second):
			t.Fatal("accepted control event was canceled before execution")
		}
	})

	t.Run("capacity", func(t *testing.T) {
		group, err := NewGroup(1, 1, 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		if err = group.Start(); err != nil {
			t.Fatal(err)
		}
		cleanupGroup(t, group)

		started := make(chan struct{})
		release := make(chan struct{})
		controlRan := make(chan struct{})
		mailbox := group.Executor(0)
		if err = mailbox.TryPost(func() { close(started); <-release }); err != nil {
			t.Fatal(err)
		}
		<-started
		if err = mailbox.TryPost(func() {}); err != nil {
			t.Fatal(err)
		}
		accepted := make(chan error, 1)
		go func() {
			accepted <- group.Post(context.Background(), 0, func() { close(controlRan) })
		}()
		select {
		case waitErr := <-accepted:
			t.Fatalf("Post returned while queue was full: %v", waitErr)
		case <-time.After(20 * time.Millisecond):
		}
		close(release)
		if waitErr := <-accepted; waitErr != nil {
			t.Fatalf("Post error = %v", waitErr)
		}
		select {
		case <-controlRan:
		case <-time.After(time.Second):
			t.Fatal("accepted control event did not run")
		}
	})

	t.Run("stop", func(t *testing.T) {
		group, err := NewGroup(1, 1, 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		if err = group.Start(); err != nil {
			t.Fatal(err)
		}

		started := make(chan struct{})
		release := make(chan struct{})
		mailbox := group.Executor(0)
		if err = mailbox.TryPost(func() { close(started); <-release }); err != nil {
			t.Fatal(err)
		}
		<-started
		if err = mailbox.TryPost(func() {}); err != nil {
			t.Fatal(err)
		}
		waitResult := make(chan error, 1)
		go func() { waitResult <- group.Post(context.Background(), 0, func() {}) }()
		stopDone := make(chan error, 1)
		go func() { stopDone <- group.Stop(context.Background()) }()
		if waitErr := <-waitResult; !errors.Is(waitErr, ErrStopped) {
			t.Fatalf("Post stop error = %v, want %v", waitErr, ErrStopped)
		}
		close(release)
		select {
		case stopErr := <-stopDone:
			if stopErr != nil {
				t.Fatalf("Stop error = %v", stopErr)
			}
		case <-time.After(time.Second):
			t.Fatal("Stop did not drain accepted jobs")
		}
	})
}

func TestMiddlewarePreservesContextAndHandlerError(t *testing.T) {
	group, err := NewGroup(1, 1, 8, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = group.Start(); err != nil {
		t.Fatal(err)
	}
	cleanupGroup(t, group)
	executor := &callCountingExecutor{Executor: group.Executor(0)}

	type contextKey struct{}
	handler := Middleware(executor)(func(ctx context.Context, _ any) (any, error) {
		if ctx.Value(contextKey{}) != "player-a" {
			return nil, errors.New("context value is missing")
		}
		return new(emptypb.Empty), nil
	})
	ctx := context.WithValue(context.Background(), contextKey{}, "player-a")
	if _, err = handler(ctx, new(emptypb.Empty)); err != nil {
		t.Fatal(err)
	}

	wantErr := errors.New("handler failed")
	failingHandler := Middleware(executor)(func(context.Context, any) (any, error) {
		return nil, wantErr
	})
	if _, err = failingHandler(context.Background(), new(emptypb.Empty)); !errors.Is(err, wantErr) {
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
