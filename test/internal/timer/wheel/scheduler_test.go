package wheel

import (
	"context"
	"testing"
	"time"
)

func BenchmarkSchedulerOnce(b *testing.B) {
	scheduler, err := New(WithTick(time.Millisecond), WithWheelSize(256))
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
