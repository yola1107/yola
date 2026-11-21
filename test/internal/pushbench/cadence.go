package pushbench

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// RunCadence 按每桌每秒一次操作测量真实 WebSocket 投递，每次操作串行广播两次。
// 每个 benchmark iteration 是一个完整场景，须使用 -benchtime=1x。
func RunCadence(b *testing.B, game string, newFanout func(Pusher, int) func(int, proto.Message)) {
	b.Helper()
	address := benchmarkRedisAddress(b)
	duration := 30 * time.Second
	if configured := os.Getenv("YOLA_CADENCE_DURATION"); configured != "" {
		var err error
		duration, err = time.ParseDuration(configured)
		require.NoError(b, err)
	}
	require.True(b, duration >= time.Second && duration <= 30*time.Minute && duration%time.Second == 0,
		"YOLA_CADENCE_DURATION must be a whole number of seconds within [1s, 30m]")
	logger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.Cleanup(func() { slog.SetDefault(logger) })
	for _, tableCount := range []int{100, 500, 1000} {
		b.Run(fmt.Sprintf("tables=%d", tableCount), func(b *testing.B) {
			require.Equal(b, 1, b.N, "use -benchtime=1x")
			measurements := newMeasurements(b)
			pipeline := newSocketPipeline(b, address, game+"-cadence-"+rand.Text(), tableCount, measurements)
			fanout := newFanout(pipeline.pusher, tableCount)
			group := benchmarkMailboxes(b, tableCount, min(tableCount, 16))
			fanout(0, cadencePayload(0, 0, 0))
			require.Eventually(b, func() bool { return pipeline.receivers[3].warmups.Load() == 1 }, 5*time.Second, time.Millisecond)
			measurements.collect(b)
			operations := int(duration/time.Second) * tableCount
			var completed atomic.Int64
			timer := time.NewTimer(0)
			defer timer.Stop()
			b.ResetTimer()
			started := time.Now()
			for operation := range operations {
				due := started.Add(time.Duration(operation) * time.Second / time.Duration(tableCount))
				timer.Reset(max(time.Until(due), 0))
				select {
				case <-timer.C:
				case <-b.Context().Done():
					b.Fatal(b.Context().Err())
				}
				measurements.record("generator_lag", due, nil)
				submitted := time.Now()
				index := operation % tableCount
				sequence := int64(operation/tableCount)*2 + 1
				// 发生器直接按计划入队，避免 goroutine 调度改变同桌任务的接纳顺序。
				err := group.Executor(index).TryPost(func() {
					measurements.record("mailbox_wait", submitted, nil)
					jobStarted := time.Now()
					for offset := range int64(2) {
						message := cadencePayload(index, sequence+offset, due.Sub(pipeline.origin))
						fanoutStarted := time.Now()
						fanout(index, message)
						measurements.record("fanout", fanoutStarted, nil)
					}
					measurements.record("job", jobStarted, nil)
					measurements.record("job_complete", submitted, nil)
					measurements.record("scheduled_complete", due, nil)
					completed.Add(1)
				})
				measurements.record("mailbox_admit", submitted, err)
			}
			drainCtx, cancelDrain := context.WithTimeout(b.Context(), 15*time.Second)
			defer cancelDrain()
			require.NoError(b, group.Stop(drainCtx))
			pipeline.waitReceived(b, int64(duration/time.Second)*2)
			elapsed := time.Since(started)
			b.StopTimer()
			b.Logf("CADENCE setup: game=%s tables=%d players=%d workers=%d interval=1s operations=%d completed=%d duration=%s elapsed=%s",
				game, tableCount, tableCount*4, min(tableCount, 16), operations, completed.Load(), duration, elapsed)
			measurements.report(b)
			require.Equal(b, int64(operations), completed.Load())
		})
	}
}

// RunSocketOrdering 验证真实桌广播在慢读和同 UID 重连后仍按接纳顺序到达。
func RunSocketOrdering(t *testing.T, game string, newFanout func(Pusher, int) func(int, proto.Message)) {
	t.Helper()
	address := benchmarkRedisAddress(t)
	measurements := newMeasurements(t)
	pipeline := newSocketPipeline(t, address, game+"-ordering-"+rand.Text(), 2, measurements)
	fanout := newFanout(pipeline.pusher, 2)
	pipeline.reconnect(t, 0, 10*time.Millisecond)
	for sequence := int64(1); sequence <= 8; sequence++ {
		for table := range 2 {
			fanout(table, cadencePayload(table, sequence, time.Since(pipeline.origin)))
		}
	}
	pipeline.waitReceived(t, 8)
	pipeline.reconnect(t, 0, 0)
	for sequence := int64(9); sequence <= 16; sequence++ {
		for table := range 2 {
			fanout(table, cadencePayload(table, sequence, time.Since(pipeline.origin)))
		}
	}
	pipeline.waitReceived(t, 16)
}
