package contextwait

import (
	"context"
	"errors"
	"testing"
)

func TestDonePrefersCompletedWork(t *testing.T) {
	done := make(chan struct{})
	close(done)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := Done(ctx, done); err != nil {
		t.Fatalf("Done() error = %v, want nil", err)
	}
}

func TestDoneReturnsContextErrorWhileWorkContinues(t *testing.T) {
	done := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := Done(ctx, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("Done() error = %v, want %v", err, context.Canceled)
	}
}
