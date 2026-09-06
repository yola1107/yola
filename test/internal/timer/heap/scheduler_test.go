package timerheap_test

import (
	"context"
	"testing"

	timerheap "yola/test/internal/timer/heap"
)

func BenchmarkSchedulerOnce(b *testing.B) {
	scheduler, err := timerheap.New()
	if err != nil {
		b.Fatal(err)
	}
	if err = scheduler.Start(context.Background()); err != nil {
		b.Fatal(err)
	}
	defer func() {
		if stopErr := scheduler.Stop(context.Background()); stopErr != nil {
			b.Error(stopErr)
		}
	}()

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		done := make(chan struct{})
		if _, err = scheduler.Once(0, func() { close(done) }); err != nil {
			b.Fatal(err)
		}
		<-done
	}
}
