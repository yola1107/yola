package gateway

import (
	"context"
	"fmt"
	"runtime"
	"testing"
	"time"

	protocolv1 "yola/api/protocol/v1"
	"yola/locate"
	"yola/network"

	"google.golang.org/protobuf/proto"
)

func BenchmarkBroadcast(b *testing.B) {
	for _, sessionCount := range []int{1_000, 10_000, 100_000} {
		b.Run(fmt.Sprintf("sessions=%d", sessionCount), func(b *testing.B) {
			sessions := newBenchmarkSessions(sessionCount)
			broadcaster, running, stop := startBenchmarkFanout(sessions)
			message := &protocolv1.Proto{Op: protocolv1.OpPush, Cmd: 1, Body: make([]byte, 256)}
			if !broadcaster.fanout(running, message) {
				b.Fatal("fanout stopped during warmup")
			}

			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if !broadcaster.fanout(running, message) {
					b.Fatal("fanout stopped")
				}
			}
			b.StopTimer()
			stop()
		})
	}
}

func BenchmarkBroadcastAdmission(b *testing.B) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	running := &broadcastRun{ctx: ctx, pushes: make(chan *protocolv1.Proto, 1)}
	server := &Server{broadcaster: &broadcaster{active: running}}
	payload := make([]byte, 256)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := server.Broadcast(42, payload); err != nil {
			b.Fatal(err)
		}
		runtime.KeepAlive(<-running.pushes)
	}
}

func BenchmarkBroadcastWebSocketEncoding(b *testing.B) {
	for _, payloadSize := range []int{256, 4_000} {
		b.Run(fmt.Sprintf("payload=%d", payloadSize), func(b *testing.B) {
			sessions := newEncodingBenchmarkSessions(100_000)
			broadcaster, running, stop := startBenchmarkFanout(sessions)
			message := &protocolv1.Proto{Op: protocolv1.OpPush, Cmd: 1, Body: make([]byte, payloadSize)}
			running.sessions = sessions.snapshot(nil)
			clear(running.sessions)
			running.sessions = running.sessions[:0]

			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if !broadcaster.fanout(running, message) {
					b.Fatal("fanout stopped")
				}
			}
			b.StopTimer()
			stop()
		})
	}
}

func startBenchmarkFanout(sessions *sessionRegistry) (*broadcaster, *broadcastRun, func()) {
	workers := max(1, runtime.GOMAXPROCS(0))
	broadcaster := newBroadcaster(sessions, workers, 1)
	ctx, cancel := context.WithCancel(context.Background())
	running := &broadcastRun{
		ctx:       ctx,
		cancel:    cancel,
		batches:   make(chan fanoutBatch, workers),
		batchDone: make(chan struct{}, workers),
	}
	running.wg.Add(workers)
	for range workers {
		go broadcaster.fanoutWorker(running)
	}
	return broadcaster, running, func() {
		running.cancel()
		running.wg.Wait()
	}
}

func newBenchmarkSessions(count int) *sessionRegistry {
	sessions := &sessionRegistry{byConnID: make(map[string]*session, count)}
	for index := range count {
		connID := fmt.Sprintf("conn-%d", index)
		binding := locate.GateBinding{
			ServiceName: "game", UID: connID, BindingToken: connID,
			GateID: "gate-a", GateEndpoint: "grpc://127.0.0.1:9000", ConnID: connID,
		}
		sessions.byConnID[connID] = &session{
			conn: benchmarkConnection{connID: connID}, binding: binding,
			leaseDeadline: time.Now().Add(time.Hour),
		}
	}
	return sessions
}

func newEncodingBenchmarkSessions(count int) *sessionRegistry {
	sessions := newBenchmarkSessions(count)
	for connID, sess := range sessions.byConnID {
		sess.conn = encodingBenchmarkConnection{connID: connID}
	}
	return sessions
}

type benchmarkConnection struct {
	connID string
}

func (c benchmarkConnection) ConnID() string                                        { return c.connID }
func (benchmarkConnection) RemoteAddr() string                                      { return "127.0.0.1:5000" }
func (benchmarkConnection) SendProto(*protocolv1.Proto) error                       { return nil }
func (benchmarkConnection) CloseWithProto(context.Context, *protocolv1.Proto) error { return nil }
func (benchmarkConnection) Close() error                                            { return nil }

type encodingBenchmarkConnection struct {
	connID string
}

func (c encodingBenchmarkConnection) ConnID() string   { return c.connID }
func (encodingBenchmarkConnection) RemoteAddr() string { return "127.0.0.1:5000" }
func (encodingBenchmarkConnection) SendProto(message *protocolv1.Proto) error {
	encoded, err := proto.Marshal(message)
	runtime.KeepAlive(encoded)
	return err
}

func (encodingBenchmarkConnection) SendPrepared(message *network.PreparedProto) error {
	encoded, err := message.Marshal()
	runtime.KeepAlive(encoded)
	return err
}

func (encodingBenchmarkConnection) CloseWithProto(context.Context, *protocolv1.Proto) error {
	return nil
}
func (encodingBenchmarkConnection) Close() error { return nil }
