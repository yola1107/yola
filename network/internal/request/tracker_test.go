package request

import (
	"context"
	"errors"
	"testing"

	"yola/api/protocol/v1"
)

func TestTrackerResolve(t *testing.T) {
	var tracker Tracker
	message, pending := tracker.Begin(7, []byte("request"))
	if message.Op != v1.OpRequest || message.Cmd != 7 || message.Seq == 0 || string(message.Body) != "request" {
		t.Fatalf("Begin() message = %+v", message)
	}
	if !tracker.Resolve(&v1.Proto{Seq: message.Seq, Code: 9, Body: []byte("reply")}) {
		t.Fatal("Resolve() did not find the pending request")
	}
	body, code, err := pending.Wait(context.Background())
	if err != nil || string(body) != "reply" || code != 9 {
		t.Fatalf("Wait() = %q, %d, %v", body, code, err)
	}
}

func TestTrackerFail(t *testing.T) {
	var tracker Tracker
	_, pending := tracker.Begin(1, nil)
	want := errors.New("connection closed")
	tracker.Fail(want)
	_, _, err := pending.Wait(context.Background())
	if !errors.Is(err, want) {
		t.Fatalf("Wait() error = %v, want %v", err, want)
	}
}

func TestPendingCancelAndDeadline(t *testing.T) {
	var tracker Tracker
	message, pending := tracker.Begin(1, nil)
	pending.Cancel()
	if tracker.Resolve(&v1.Proto{Seq: message.Seq}) {
		t.Fatal("Resolve() found a canceled request")
	}

	message, pending = tracker.Begin(1, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()
	_, _, err := pending.Wait(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait() error = %v, want deadline exceeded", err)
	}
	if tracker.Resolve(&v1.Proto{Seq: message.Seq}) {
		t.Fatal("Resolve() found a timed-out request")
	}
}

func TestTrackerSkipsSequenceStillInUseAfterWrap(t *testing.T) {
	var tracker Tracker
	firstMessage, first := tracker.Begin(1, nil)
	tracker.seq.Store(1<<31 - 1)
	secondMessage, second := tracker.Begin(2, nil)
	if secondMessage.Seq == firstMessage.Seq {
		t.Fatalf("Begin() reused pending sequence %d", secondMessage.Seq)
	}
	first.Cancel()
	second.Cancel()
}
