package stdlib

import (
	"context"
	"testing"
)

func BenchmarkSchedulerOnce(b *testing.B) {
	scheduler := New()
	if err := scheduler.Start(context.Background()); err != nil {
		b.Fatal(err)
	}
	defer func() {
		if err := scheduler.Stop(context.Background()); err != nil {
			b.Error(err)
		}
	}()

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		done := make(chan struct{})
		if _, err := scheduler.Once(0, func() { close(done) }); err != nil {
			b.Fatal(err)
		}
		<-done
	}
}
