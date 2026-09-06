// Package pushbench 测量真实 Redis、Node、Gateway 与桌 mailbox 之间的同步广播成本。
package pushbench

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"yola/test/internal/mailbox"

	"github.com/stretchr/testify/require"
)

// Run 用真实桌广播函数测量分段耗时；调用者按给定桌数装配每桌四名玩家。
// Redis 必须是可丢弃专用实例；随机 service 隔离每个子基准的 binding 和 epoch。
func Run(b *testing.B, game string, newFanout func(Pusher, int) func(int)) {
	b.Helper()
	address := os.Getenv("YOLA_REDIS_INTEGRATION")
	if address == "" {
		b.Skip("set YOLA_REDIS_INTEGRATION to a disposable Redis instance")
	}
	logger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.Cleanup(func() { slog.SetDefault(logger) })
	for _, tableCount := range []int{8, 64} {
		for _, delay := range []time.Duration{0, 5 * time.Millisecond} {
			b.Run(fmt.Sprintf("tables=%d/gateway_delay=%s", tableCount, delay), func(b *testing.B) {
				measurements := newMeasurements(b)
				pusher, received := newPipeline(b, address, game+"-"+rand.Text(), tableCount*4, delay, measurements)
				fanout := newFanout(pusher, tableCount)
				workers := min(tableCount, 16)
				group, err := mailbox.NewGroup(tableCount, workers, 128, 64)
				require.NoError(b, err)
				require.NoError(b, group.Start())
				b.Cleanup(func() {
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					require.NoError(b, group.Stop(ctx))
				})
				var next atomic.Uint64
				// 闭环请求并发固定为 8 × GOMAXPROCS，使共享 worker 的排队成本可见。
				b.SetParallelism(8)
				b.ReportAllocs()
				b.ResetTimer()
				b.RunParallel(func(pb *testing.PB) {
					for pb.Next() {
						index := int((next.Add(1) - 1) % uint64(tableCount))
						submitted := time.Now()
						err := group.Executor(index).Call(context.Background(), func() error {
							measurements.record("mailbox_wait", submitted, nil)
							started := time.Now()
							fanout(index)
							measurements.record("fanout", started, nil)
							return nil
						})
						measurements.record("mailbox_call", submitted, err)
					}
				})
				b.StopTimer()
				b.Logf("I45 setup: tables=%d players=%d workers=%d concurrency=%d queue=128 batch=64 gateway_delay=%s",
					tableCount, tableCount*4, workers, runtime.GOMAXPROCS(0)*8, delay)
				measurements.report(b)
				require.Equal(b, uint64(b.N)*4, received.Load(), "every fanout must deliver four pushes")
			})
		}
	}
}
