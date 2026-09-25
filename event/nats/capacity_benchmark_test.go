package nats

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"yola/event"

	natsgo "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// BenchmarkSubscriptionCapacity 隔离发布进程，测量指定外置 broker 下接收进程的积压与释放。
// 每个子场景须用独立进程、-benchtime=1x 运行；RSS 读取 Linux procfs。
func BenchmarkSubscriptionCapacity(b *testing.B) {
	url := os.Getenv("YOLA_NATS_URL")
	if url == "" || runtime.GOOS != "linux" {
		b.Skip("requires Linux and a dedicated external YOLA_NATS_URL")
	}
	for _, test := range []struct {
		name          string
		payload       int
		capacity      int
		subscriptions int
		overflow      int
		closeQueued   bool
	}{
		{name: "small", payload: 256, capacity: 256, subscriptions: 1},
		{name: "default", payload: 64 << 10, capacity: 256, subscriptions: 1, overflow: 64},
		{name: "four", payload: 64 << 10, capacity: 256, subscriptions: 4, overflow: 64},
		{name: "oversized", payload: 1 << 20, capacity: 256, subscriptions: 1, overflow: 64},
		{name: "close_default", payload: 64 << 10, capacity: 256, subscriptions: 1, overflow: 64, closeQueued: true},
		{name: "close_oversized", payload: 1 << 20, capacity: 256, subscriptions: 1, overflow: 64, closeQueued: true},
	} {
		b.Run(test.name, func(b *testing.B) {
			require.Equal(b, 1, b.N, "run each capacity case in a fresh process with -benchtime=1x")
			b.StopTimer()
			bus := newTestBus(b, url, WithQueueCapacity(test.capacity))
			require.GreaterOrEqual(b, bus.conn.MaxPayload(), int64(test.payload))
			publish := startCapacityPublisher(b)
			topic := natsgo.NewInbox()
			started := make(chan struct{}, test.subscriptions)
			release := make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			b.Cleanup(unblock)
			var durationsMu sync.Mutex
			durations := make([]time.Duration, 0, test.capacity*test.subscriptions)
			subs := make([]event.SubscriptionStatsProvider, test.subscriptions)
			for i := range subs {
				sub, err := bus.Subscribe(context.Background(), topic, func(ctx context.Context, received event.Event) {
					if len(received.Payload) == 1 {
						started <- struct{}{}
						select {
						case <-release:
						case <-ctx.Done():
						}
						return
					}
					// 固定 2ms 的协作 handler 是诊断模型，不代表业务 SLO。
					began := time.Now()
					timer := time.NewTimer(2 * time.Millisecond)
					defer timer.Stop()
					select {
					case <-timer.C:
					case <-ctx.Done():
					}
					duration := time.Since(began)
					durationsMu.Lock()
					durations = append(durations, duration)
					durationsMu.Unlock()
				})
				require.NoError(b, err)
				subs[i] = sub.(event.SubscriptionStatsProvider)
			}
			publish(topic, 1, 1)
			for range subs {
				select {
				case <-started:
				case <-time.After(5 * time.Second):
					b.Fatal("primer did not reach every handler")
				}
			}
			runtime.GC()
			peakRSS := reportCapacityMemory(b, "base")
			b.StartTimer()
			fillSubscriptionBacklog(b, subs, test.capacity, test.overflow, func(count int) {
				publish(topic, test.payload, count)
			})
			b.StopTimer()
			runtime.GC()
			peakRSS = max(peakRSS, reportCapacityMemory(b, "full"))
			b.ReportMetric(float64(test.capacity*test.payload*test.subscriptions), "queued-payload-B")
			b.ReportMetric(float64(test.overflow*test.subscriptions), "queue-dropped")
			b.ReportMetric(float64(bus.conn.MaxPayload()), "broker-max-B")
			b.ReportMetric(float64(test.subscriptions), "subscriptions")
			began := time.Now()
			if !test.closeQueued {
				unblock()
				require.Eventually(b, func() bool {
					for _, sub := range subs {
						stats := sub.SubscriptionStats()
						if stats.QueueDepth != 0 || stats.HandlerActive {
							return false
						}
						if test.payload > 64<<10 {
							if stats.PayloadDropped != uint64(test.capacity) || stats.HandlerCalls != 1 {
								return false
							}
						} else if stats.HandlerCalls != uint64(test.capacity+1) {
							return false
						}
					}
					return true
				}, 10*time.Second, time.Millisecond)
				b.ReportMetric(float64(time.Since(began).Microseconds()), "drain-us")
			}
			closeBegan := time.Now()
			require.NoError(b, bus.Close())
			b.ReportMetric(float64(time.Since(closeBegan).Microseconds()), "close-us")
			var payloadDropped, handlerCalls uint64
			for _, sub := range subs {
				stats := sub.SubscriptionStats()
				payloadDropped += stats.PayloadDropped
				handlerCalls += stats.HandlerCalls
				require.True(b, stats.Closed)
				require.Zero(b, stats.QueueDepth)
				require.False(b, stats.HandlerActive)
				if test.closeQueued {
					require.Equal(b, uint64(1), stats.HandlerCalls)
					require.Zero(b, stats.PayloadDropped)
				}
			}
			b.ReportMetric(float64(payloadDropped), "payload-dropped")
			b.ReportMetric(float64(handlerCalls), "handler-calls")
			// 分开报告自然关闭、GC 与主动 scavenging；不把 RSS 回落视为 Close 保证。
			time.Sleep(100 * time.Millisecond)
			peakRSS = max(peakRSS, reportCapacityMemory(b, "closed"))
			runtime.GC()
			peakRSS = max(peakRSS, reportCapacityMemory(b, "gc"))
			debug.FreeOSMemory()
			peakRSS = max(peakRSS, reportCapacityMemory(b, "scavenged"))
			b.ReportMetric(float64(peakRSS), "peak-rss-B")
			runtime.KeepAlive(subs)
			durationsMu.Lock()
			expectedSamples := 0
			if !test.closeQueued && test.payload <= 64<<10 {
				expectedSamples = test.capacity * test.subscriptions
			}
			require.Len(b, durations, expectedSamples)
			slices.Sort(durations)
			b.ReportMetric(float64(len(durations)), "handler-samples")
			if len(durations) > 0 {
				b.ReportMetric(float64(durations[(len(durations)*99+99)/100-1].Nanoseconds()), "handler-p99-ns")
			}
			durationsMu.Unlock()
		})
	}
}

// fillSubscriptionBacklog 等待每批抵达接收队列；handler 必须已经被调用方的屏障阻塞。
func fillSubscriptionBacklog(b *testing.B, subs []event.SubscriptionStatsProvider, capacity, overflow int, publish func(int)) {
	b.Helper()
	for sent := 0; sent < capacity+overflow; {
		batch := min(16, capacity+overflow-sent)
		publish(batch)
		sent += batch
		// Flush 只确认 broker 收到发布；接收计数避免把 broker 断连混作本地队列拒绝。
		require.Eventually(b, func() bool {
			for _, sub := range subs {
				stats := sub.SubscriptionStats()
				if !stats.QueueDroppedCurrent || uint64(stats.QueueDepth)+stats.QueueDropped != uint64(sent) {
					return false
				}
			}
			return true
		}, 10*time.Second, time.Millisecond)
	}
	for _, sub := range subs {
		stats := sub.SubscriptionStats()
		require.Equal(b, capacity, stats.QueueDepth)
		require.Equal(b, uint64(overflow), stats.QueueDropped)
	}
}

func reportCapacityMemory(b *testing.B, phase string) uint64 {
	b.Helper()
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	processStatus, err := os.ReadFile("/proc/self/status")
	require.NoError(b, err)
	var resident, peak uint64
	for _, line := range strings.Split(string(processStatus), "\n") {
		if value, ok := strings.CutPrefix(line, "VmRSS:"); ok {
			_, err = fmt.Sscan(value, &resident)
			require.NoError(b, err)
		}
		if value, ok := strings.CutPrefix(line, "VmHWM:"); ok {
			_, err = fmt.Sscan(value, &peak)
			require.NoError(b, err)
		}
	}
	require.Positive(b, resident)
	require.GreaterOrEqual(b, peak, resident)
	b.ReportMetric(float64(stats.HeapAlloc), phase+"-heap-B")
	b.ReportMetric(float64(resident*1024), phase+"-rss-B")
	// 调用方保留各阶段 VmHWM 的最大值，避免后续采样覆盖已经观察到的高水位。
	return peak * 1024
}

type capacityPublishBatch struct {
	Topic string
	Size  int
	Count int
}

// TestCapacityPublisherProcess 只作为 benchmark 的发布子进程运行，避免发布内存计入接收方 RSS。
func TestCapacityPublisherProcess(t *testing.T) {
	if os.Getenv("YOLA_NATS_CAPACITY_PUBLISHER") != "1" {
		t.Skip("capacity benchmark subprocess only")
	}
	conn, err := natsgo.Connect(os.Getenv("YOLA_NATS_URL"), natsgo.NoReconnect())
	require.NoError(t, err)
	defer conn.Close()
	decoder := json.NewDecoder(bufio.NewReader(os.Stdin))
	encoder := json.NewEncoder(os.Stdout)
	for {
		var batch capacityPublishBatch
		if err = decoder.Decode(&batch); errors.Is(err, io.EOF) {
			return
		}
		require.NoError(t, err)
		require.GreaterOrEqual(t, batch.Size, 1)
		require.LessOrEqual(t, int64(batch.Size), conn.MaxPayload())
		require.Greater(t, batch.Count, 0)
		require.LessOrEqual(t, batch.Count, 4096)
		require.True(t, event.ValidTopic(batch.Topic))
		payload := make([]byte, batch.Size)
		for range batch.Count {
			require.NoError(t, conn.Publish(batch.Topic, payload))
			// 限制 broker 的在途批量，当前基准测本地接收积压，不测发布端突发极限。
			require.NoError(t, conn.FlushTimeout(5*time.Second))
		}
		require.NoError(t, encoder.Encode(batch.Count))
	}
}

func startCapacityPublisher(b *testing.B) func(string, int, int) {
	b.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	b.Cleanup(cancel)
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCapacityPublisherProcess$")
	command.Env = append(os.Environ(), "YOLA_NATS_CAPACITY_PUBLISHER=1")
	input, err := command.StdinPipe()
	require.NoError(b, err)
	output, err := command.StdoutPipe()
	require.NoError(b, err)
	command.Stderr = os.Stderr
	require.NoError(b, command.Start())
	b.Cleanup(func() {
		require.NoError(b, input.Close())
		require.NoError(b, command.Wait())
	})
	encoder := json.NewEncoder(input)
	decoder := json.NewDecoder(output)
	return func(topic string, size, count int) {
		b.Helper()
		require.NoError(b, encoder.Encode(capacityPublishBatch{Topic: topic, Size: size, Count: count}))
		var published int
		require.NoError(b, decoder.Decode(&published))
		require.Equal(b, count, published)
	}
}
