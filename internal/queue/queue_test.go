package queue

import (
	"errors"
	"testing"
	"time"
)

func TestStopLetsRunningCallbackFinishAndDiscardsPending(t *testing.T) {
	queue := New(WithCapacity(1))
	running := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	pendingRan := make(chan struct{}, 1)
	workerDone := make(chan struct{})
	go func() {
		queue.Run()
		close(workerDone)
	}()
	if err := queue.Submit(func() {
		close(running)
		<-release
		close(finished)
	}); err != nil {
		t.Fatal(err)
	}
	waitSignal(t, running)
	if err := queue.Submit(func() { pendingRan <- struct{}{} }); err != nil {
		t.Fatal(err)
	}
	queue.Stop()
	close(release)
	waitSignal(t, finished)
	waitSignal(t, workerDone)
	select {
	case <-pendingRan:
		t.Fatal("pending callback ran after Stop")
	default:
	}
	if err := queue.Submit(func() {}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Submit() error = %v, want %v", err, ErrClosed)
	}
}

func TestBeginTerminationRunsAfterCurrentCallbackAndDiscardsPending(t *testing.T) {
	queue := New(WithCapacity(1))
	running := make(chan struct{})
	release := make(chan struct{})
	pendingRan := make(chan struct{}, 1)
	terminalRan := make(chan struct{})
	go queue.Run()
	requireSubmit(t, queue, func() {
		close(running)
		<-release
	})
	waitSignal(t, running)
	requireSubmit(t, queue, func() { pendingRan <- struct{}{} })
	finish := queue.BeginTermination(func() { close(terminalRan) })
	finish()
	select {
	case <-terminalRan:
		t.Fatal("terminal callback overlapped the running callback")
	default:
	}
	close(release)
	waitSignal(t, terminalRan)
	select {
	case <-pendingRan:
		t.Fatal("pending callback ran during termination")
	default:
	}
}

func TestSubmitBatchIsAtomic(t *testing.T) {
	queue := New(WithCapacity(2))
	runs := make(chan string, 2)
	if err := queue.SubmitBatch(func() { runs <- "first" }, func() { runs <- "second" }); err != nil {
		t.Fatal(err)
	}
	if err := queue.SubmitBatch(func() {}, func() {}); !errors.Is(err, ErrFull) {
		t.Fatalf("oversized SubmitBatch() error = %v, want %v", err, ErrFull)
	}
	if err := queue.SubmitBatch(func() {}, nil); !errors.Is(err, ErrNilCallback) {
		t.Fatalf("nil SubmitBatch() error = %v, want %v", err, ErrNilCallback)
	}

	go queue.Run()
	if first, second := waitValue(t, runs), waitValue(t, runs); first != "first" || second != "second" {
		t.Fatalf("callback order = %q, %q", first, second)
	}
	queue.Stop()
}

func TestTerminalCallbacksIsolatePanicsAndPreserveOrder(t *testing.T) {
	panicked := make(chan any, 1)
	runs := make(chan string, 2)
	queue := New(WithPanicHandler(func(value any, _ []byte) { panicked <- value }))
	go queue.Run()
	finish := queue.BeginTermination(
		func() {
			runs <- "first"
			panic("boom")
		},
		func() { runs <- "second" },
	)
	finish()
	if first, second := waitValue(t, runs), waitValue(t, runs); first != "first" || second != "second" {
		t.Fatalf("terminal callback order = %q, %q", first, second)
	}
	if value := waitValue(t, panicked); value != "boom" {
		t.Fatalf("panic value = %v, want boom", value)
	}
}

func TestBeginTerminationCopiesCallbackSlice(t *testing.T) {
	ran := make(chan string, 1)
	terminals := []func(){func() { ran <- "original" }}
	queue := New()
	finish := queue.BeginTermination(terminals...)
	terminals[0] = func() { ran <- "changed" }
	go queue.Run()
	finish()
	if got := waitValue(t, ran); got != "original" {
		t.Fatalf("terminal callback = %q, want original", got)
	}
}

func TestCallbackCanBeginTermination(t *testing.T) {
	queue := New()
	terminalRan := make(chan struct{})
	go queue.Run()
	requireSubmit(t, queue, func() {
		finish := queue.BeginTermination(func() { close(terminalRan) })
		finish()
	})
	waitSignal(t, terminalRan)
}

func TestBeginTerminationWaitsForRelease(t *testing.T) {
	queue := New()
	terminalRan := make(chan struct{})
	go queue.Run()

	finish := queue.BeginTermination(func() { close(terminalRan) })
	select {
	case <-terminalRan:
		t.Fatal("terminal callback ran before release")
	default:
	}
	finish()
	waitSignal(t, terminalRan)
}

func TestRunRecoversCallbackPanic(t *testing.T) {
	panicValue := make(chan any, 1)
	nextCallbackDone := make(chan struct{})
	queue := New(WithPanicHandler(func(value any, _ []byte) {
		panicValue <- value
	}))
	go queue.Run()

	if err := queue.Submit(func() { panic("boom") }); err != nil {
		t.Fatal(err)
	}
	if err := queue.Submit(func() { close(nextCallbackDone) }); err != nil {
		t.Fatal(err)
	}
	if value := waitValue(t, panicValue); value != "boom" {
		t.Fatalf("panic value = %v, want boom", value)
	}
	waitSignal(t, nextCallbackDone)
	queue.Stop()
}

func TestSubmitRejectsNilCallback(t *testing.T) {
	panicked := make(chan any, 1)
	queue := New(WithCapacity(1), WithPanicHandler(func(value any, _ []byte) {
		panicked <- value
	}))

	if err := queue.Submit(nil); !errors.Is(err, ErrNilCallback) {
		t.Fatalf("Submit(nil) error = %v, want %v", err, ErrNilCallback)
	}

	callbackDone := make(chan struct{})
	if err := queue.Submit(func() { close(callbackDone) }); err != nil {
		t.Fatalf("Submit(valid callback) error = %v, want nil", err)
	}

	workerDone := make(chan struct{})
	go func() {
		queue.Run()
		close(workerDone)
	}()
	waitSignal(t, callbackDone)
	select {
	case value := <-panicked:
		t.Fatalf("PanicHandler value = %v, want no call for nil callback", value)
	default:
	}
	queue.Stop()
	waitSignal(t, workerDone)
	if err := queue.Submit(nil); !errors.Is(err, ErrNilCallback) {
		t.Fatalf("Submit(nil) after Stop error = %v, want %v", err, ErrNilCallback)
	}
}

func TestStopWakesIdleRun(t *testing.T) {
	queue := New()
	workerDone := make(chan struct{})
	go func() {
		queue.Run()
		close(workerDone)
	}()
	queue.Stop()
	waitSignal(t, workerDone)
}

func TestNewRejectsInvalidCapacity(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("New() did not panic")
		}
	}()
	New(WithCapacity(0))
}

func waitSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	waitValue(t, signal)
}

func requireSubmit(t *testing.T, queue *Queue, callback func()) {
	t.Helper()
	if err := queue.Submit(callback); err != nil {
		t.Fatal(err)
	}
}

func waitValue[T any](t *testing.T, values <-chan T) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for channel value")
		var zero T
		return zero
	}
}
