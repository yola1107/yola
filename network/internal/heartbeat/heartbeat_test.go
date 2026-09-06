package heartbeat

import (
	"sync"
	"testing"
)

func TestStateLifecycle(t *testing.T) {
	var state State
	if got := state.Tick(); got != TickQueue {
		t.Fatalf("first Tick() = %v, want %v", got, TickQueue)
	}
	if got := state.Tick(); got != TickPending {
		t.Fatalf("queued Tick() = %v, want %v", got, TickPending)
	}
	if !state.BeginWrite() {
		t.Fatal("BeginWrite() rejected a queued heartbeat")
	}
	if got := state.Tick(); got != TickPending {
		t.Fatalf("writing Tick() = %v, want %v", got, TickPending)
	}
	if !state.FinishWrite() {
		t.Fatal("FinishWrite() rejected a writing heartbeat")
	}
	if got := state.Tick(); got != TickTimeout {
		t.Fatalf("outstanding Tick() = %v, want %v", got, TickTimeout)
	}
	if state.Reply() {
		t.Fatal("Reply() accepted a timed-out heartbeat")
	}
	if got := state.Tick(); got != TickQueue {
		t.Fatalf("Tick() after timeout = %v, want %v", got, TickQueue)
	}
	if !state.CancelQueue() {
		t.Fatal("CancelQueue() rejected a queued heartbeat")
	}
	if state.BeginWrite() {
		t.Fatal("BeginWrite() accepted an idle heartbeat")
	}
}

func TestReplyCanRaceWithWriteCompletion(t *testing.T) {
	for range 100 {
		var state State
		if state.Tick() != TickQueue || !state.BeginWrite() {
			t.Fatal("failed to begin heartbeat write")
		}

		start := make(chan struct{})
		results := make(chan bool, 2)
		var workers sync.WaitGroup
		workers.Add(2)
		go func() {
			defer workers.Done()
			<-start
			results <- state.FinishWrite()
		}()
		go func() {
			defer workers.Done()
			<-start
			results <- state.Reply()
		}()
		close(start)
		workers.Wait()
		close(results)
		for result := range results {
			if !result {
				t.Fatal("concurrent write completion or reply was rejected")
			}
		}
		if got := state.Tick(); got != TickQueue {
			t.Fatalf("Tick() after reply = %v, want %v", got, TickQueue)
		}
	}
}

func TestReplyRequiresStartedWrite(t *testing.T) {
	var state State
	if state.Reply() {
		t.Fatal("Reply() accepted an idle heartbeat")
	}
	if state.Tick() != TickQueue {
		t.Fatal("failed to queue heartbeat")
	}
	if state.Reply() {
		t.Fatal("Reply() accepted a heartbeat that was not being written")
	}
	if !state.BeginWrite() || !state.Reply() || !state.FinishWrite() {
		t.Fatal("reply during write did not complete the heartbeat")
	}
	if state.Reply() {
		t.Fatal("Reply() acknowledged the same heartbeat twice")
	}
}

func TestOldWriteCompletionPreservesNextQueuedHeartbeat(t *testing.T) {
	var state State
	if state.Tick() != TickQueue || !state.BeginWrite() || !state.Reply() {
		t.Fatal("failed to acknowledge heartbeat during its write")
	}
	if got := state.Tick(); got != TickQueue {
		t.Fatalf("Tick() after early reply = %v, want %v", got, TickQueue)
	}
	if !state.FinishWrite() {
		t.Fatal("old FinishWrite() rejected a queued next heartbeat")
	}
	if !state.BeginWrite() {
		t.Fatal("old FinishWrite() overwrote the next queued heartbeat")
	}
}
