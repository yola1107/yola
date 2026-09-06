package nats

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"yola/event"

	natsgo "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

func BenchmarkPublish(b *testing.B) {
	bus := newTestBus(b, benchmarkURL(b))
	published := event.Event{Topic: "yola.event.benchmark", Payload: make([]byte, 256)}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := bus.Publish(context.Background(), published); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	require.NoError(b, bus.conn.Flush())
}

func BenchmarkEndToEnd(b *testing.B) {
	bus := newTestBus(b, benchmarkURL(b), WithQueueCapacity(65536))
	topic := natsgo.NewInbox()
	var received atomic.Int64
	done := make(chan struct{})
	_, err := bus.Subscribe(context.Background(), topic, func(context.Context, event.Event) {
		if received.Add(1) == int64(b.N) {
			close(done)
		}
	})
	require.NoError(b, err)
	published := event.Event{Topic: topic, Payload: make([]byte, 256)}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := bus.Publish(context.Background(), published); err != nil {
			b.Fatal(err)
		}
	}
	require.NoError(b, bus.conn.Flush())
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		b.Fatalf("received %d of %d events", received.Load(), b.N)
	}
}

func BenchmarkDispatch(b *testing.B) {
	registered := &subscription{
		topic:           "yola.event.benchmark",
		handler:         func(context.Context, event.Event) {},
		maxPayloadBytes: 1 << 20,
	}
	message := &natsgo.Msg{Subject: registered.topic, Data: make([]byte, 256)}
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		registered.handle(ctx, message)
	}
}

func BenchmarkSubscriptionBacklogMemory(b *testing.B) {
	tests := []struct {
		capacity    int
		payloadSize int
	}{
		{capacity: 1024, payloadSize: 256},
		{capacity: 256, payloadSize: 64 << 10},
		{capacity: 64, payloadSize: 1 << 20},
	}
	url := benchmarkURL(b)
	for _, test := range tests {
		name := fmt.Sprintf("capacity=%d/payload=%d", test.capacity, test.payloadSize)
		b.Run(name, func(b *testing.B) {
			var heapGrowth, dropped uint64
			b.SetBytes(int64(test.capacity * test.payloadSize))
			b.ReportAllocs()
			for range b.N {
				growth, dropCount := measureSubscriptionBacklog(b, url, test.capacity, test.payloadSize)
				heapGrowth += growth
				dropped += dropCount
			}
			b.ReportMetric(float64(heapGrowth)/float64(b.N), "heap-growth-B/op")
			b.ReportMetric(float64(dropped)/float64(b.N), "dropped/op")
		})
	}
}

func measureSubscriptionBacklog(b *testing.B, url string, capacity, payloadSize int) (uint64, uint64) {
	b.Helper()
	b.StopTimer()
	bus, err := New(
		WithURL(url),
		WithTimeout(time.Second),
		WithQueueCapacity(capacity),
		WithMaxPayloadBytes(payloadSize),
	)
	require.NoError(b, err)
	started := make(chan struct{})
	release := make(chan struct{})
	defer func() {
		close(release)
		require.NoError(b, bus.Close())
	}()
	var blocked atomic.Bool
	subscribed, err := bus.Subscribe(context.Background(), natsgo.NewInbox(), func(context.Context, event.Event) {
		if blocked.CompareAndSwap(false, true) {
			close(started)
			<-release
		}
	})
	require.NoError(b, err)
	registered := subscribed.(*subscription)
	payload := make([]byte, payloadSize)
	published := event.Event{Topic: registered.topic, Payload: payload}
	require.NoError(b, bus.Publish(context.Background(), published))
	require.NoError(b, bus.conn.Flush())
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		b.Fatal("backlog handler did not start")
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	overflow := max(16, capacity/4)
	b.StartTimer()
	for range capacity + overflow {
		if err = bus.Publish(context.Background(), published); err != nil {
			b.Fatal(err)
		}
	}
	require.NoError(b, bus.conn.Flush())
	native := registered.native
	var dropCount int
	require.Eventually(b, func() bool {
		currentDropped, dropErr := native.Dropped()
		dropCount = currentDropped
		return dropErr == nil && currentDropped > 0
	}, 5*time.Second, time.Millisecond)
	b.StopTimer()
	b.ReportMetric(float64(capacity*payloadSize), "payload-pending-B")

	runtime.GC()
	runtime.ReadMemStats(&after)
	if after.HeapAlloc <= before.HeapAlloc {
		return 0, uint64(dropCount)
	}
	return after.HeapAlloc - before.HeapAlloc, uint64(dropCount)
}

func benchmarkURL(t testing.TB) string {
	if url := os.Getenv("YOLA_NATS_URL"); url != "" {
		return url
	}
	return startTestServer(t)
}
