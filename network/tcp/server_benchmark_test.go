package tcp

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yola/api/protocol/v1"
	"yola/network"

	"google.golang.org/protobuf/proto"
)

type tcpBenchmarkHandler struct {
	opened chan network.Connection
}

func (h *tcpBenchmarkHandler) Open(_ context.Context, conn network.Connection) error {
	h.opened <- conn
	return nil
}

func (*tcpBenchmarkHandler) Handle(_ context.Context, _ network.Connection, message *v1.Proto) (*v1.Proto, error) {
	return message, nil
}

func (*tcpBenchmarkHandler) Close(context.Context, network.Connection) {}

type tcpBenchmarkClient struct {
	conn   net.Conn
	reader *bufio.Reader
	frame  [v1.MaxProtoSize]byte
	mu     sync.Mutex
}

func (c *tcpBenchmarkClient) request(frame []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := writeBenchmarkFrame(c.conn, frame); err != nil {
		return err
	}
	return c.readFrame()
}

func (c *tcpBenchmarkClient) readPush(send func() error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := send(); err != nil {
		return err
	}
	return c.readFrame()
}

func (c *tcpBenchmarkClient) readFrame() error {
	var prefix [framePrefixSize]byte
	if _, err := io.ReadFull(c.reader, prefix[:]); err != nil {
		return err
	}
	length := binary.LittleEndian.Uint32(prefix[:])
	if length == 0 || length > v1.MaxProtoSize {
		return errFrameLength
	}
	_, err := io.ReadFull(c.reader, c.frame[:length])
	return err
}

type tcpBenchmarkServer struct {
	server      *Server
	done        chan error
	clients     []*tcpBenchmarkClient
	connections []network.Connection
}

func BenchmarkTCPServer(b *testing.B) {
	logger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.Cleanup(func() { slog.SetDefault(logger) })

	for _, count := range []int{1000, 5000, 10000} {
		b.Run(fmt.Sprintf("connections=%d", count), func(b *testing.B) {
			b.StopTimer()
			env := newTCPBenchmarkServer(b, count)
			b.Cleanup(func() { env.close(b) })
			for _, payloadSize := range []int{32, 4000} {
				request := benchmarkProto(v1.OpRequest, payloadSize)
				requestFrame := marshalBenchmarkFrame(b, request)
				push := benchmarkProto(v1.OpPush, payloadSize)
				pushBytes := int64(len(marshalBenchmarkFrame(b, push)))
				b.Run(fmt.Sprintf("request/payload=%d", payloadSize), func(b *testing.B) {
					b.SetBytes(int64(len(requestFrame)))
					runTCPBenchmark(b, len(env.clients), func(index int) error {
						return env.clients[index].request(requestFrame)
					})
				})
				b.Run(fmt.Sprintf("push/payload=%d", payloadSize), func(b *testing.B) {
					b.SetBytes(pushBytes)
					runTCPBenchmark(b, len(env.clients), func(index int) error {
						return env.clients[index].readPush(func() error { return env.connections[index].SendProto(push) })
					})
				})
			}
		})
	}
}

func BenchmarkTCPSlowConsumerBackpressure(b *testing.B) {
	p := benchmarkProto(v1.OpPush, 4000)
	b.ReportAllocs()
	for b.Loop() {
		ch := newChannel(defaultSendQueueSize, defaultCodec())
		for range defaultSendQueueSize {
			if err := ch.push(p); err != nil {
				b.Fatal(err)
			}
		}
		if err := ch.push(p); !errors.Is(err, network.ErrSendQueueFull) {
			b.Fatalf("push() error = %v, want %v", err, network.ErrSendQueueFull)
		}
	}
}

func newTCPBenchmarkServer(b *testing.B, count int) *tcpBenchmarkServer {
	b.Helper()
	handler := &tcpBenchmarkHandler{opened: make(chan network.Connection, count)}
	s := newTCPServer(
		b,
		handler,
		Address("127.0.0.1:0"),
		MaxConnLimit(int32(count)),
		MaxConnPerIP(int32(count)),
		HandshakeTimeout(time.Hour),
		HeartbeatTimeout(time.Hour),
	)
	endpoint, err := s.Endpoint()
	if err != nil {
		b.Fatal(err)
	}
	env := &tcpBenchmarkServer{server: s, done: make(chan error, 1)}
	go func() { env.done <- s.Start(context.Background()) }()
	for range count {
		conn, dialErr := net.DialTimeout("tcp", endpoint.Host, 5*time.Second)
		if dialErr != nil {
			env.close(b)
			b.Fatal(dialErr)
		}
		env.clients = append(env.clients, &tcpBenchmarkClient{conn: conn, reader: bufio.NewReaderSize(conn, defaultIOBufferSize)})
		select {
		case serverConn := <-handler.opened:
			env.connections = append(env.connections, serverConn)
		case startErr := <-env.done:
			b.Fatalf("TCP server stopped during setup: %v", startErr)
		case <-time.After(5 * time.Second):
			b.Fatal("TCP server did not open benchmark connection")
		}
	}
	return env
}

func (env *tcpBenchmarkServer) close(b *testing.B) {
	b.Helper()
	for _, client := range env.clients {
		_ = client.conn.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := env.server.Stop(ctx); err != nil {
		b.Errorf("Stop() error = %v", err)
	}
	select {
	case err := <-env.done:
		if err != nil {
			b.Errorf("Start() error = %v", err)
		}
	case <-ctx.Done():
		b.Error("TCP server did not stop")
	}
}

func runTCPBenchmark(b *testing.B, connections int, operation func(int) error) {
	var cursor atomic.Uint64
	var failed atomic.Bool
	var firstErr error
	var errOnce sync.Once
	var sampleMu sync.Mutex
	samples := make([]time.Duration, 0, max(1, b.N/128))
	var before, after runtime.MemStats
	debug.FreeOSMemory()
	rssBefore := processRSS()
	runtime.ReadMemStats(&before)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		local := 0
		for pb.Next() && !failed.Load() {
			index := int((cursor.Add(1) - 1) % uint64(connections))
			started := time.Time{}
			if local%128 == 0 {
				started = time.Now()
			}
			if err := operation(index); err != nil {
				errOnce.Do(func() { firstErr = err })
				failed.Store(true)
				return
			}
			if !started.IsZero() {
				sampleMu.Lock()
				samples = append(samples, time.Since(started))
				sampleMu.Unlock()
			}
			local++
		}
	})
	b.StopTimer()
	runtime.ReadMemStats(&after)
	if firstErr != nil {
		b.Fatal(firstErr)
	}
	reportTCPMetrics(b, samples, before, after, rssBefore, processRSS())
}

func reportTCPMetrics(b *testing.B, samples []time.Duration, before, after runtime.MemStats, rssStart, rssEnd uint64) {
	b.Helper()
	if len(samples) > 0 {
		sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
		b.ReportMetric(float64(samples[(len(samples)*99-1)/100].Nanoseconds()), "p99-ns")
	}
	b.ReportMetric(float64(after.PauseTotalNs-before.PauseTotalNs), "gc-pause-ns")
	if rssEnd > 0 {
		b.ReportMetric(float64(rssEnd), "rss-B")
		b.ReportMetric(float64(rssStart), "rss-base-B")
		if rssEnd > rssStart {
			b.ReportMetric(float64(rssEnd-rssStart), "rss-growth-B")
		}
	}
}

func processRSS() uint64 {
	stat, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(stat))
	if len(fields) < 2 {
		return 0
	}
	pages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	return pages * uint64(os.Getpagesize())
}

func benchmarkProto(operation int32, payloadSize int) *v1.Proto {
	return &v1.Proto{Op: operation, Cmd: 1, Body: make([]byte, payloadSize)}
}

func marshalBenchmarkFrame(b *testing.B, p *v1.Proto) []byte {
	b.Helper()
	body, err := proto.Marshal(p)
	if err != nil {
		b.Fatal(err)
	}
	frame := make([]byte, framePrefixSize+len(body))
	binary.LittleEndian.PutUint32(frame, uint32(len(body)))
	copy(frame[framePrefixSize:], body)
	return frame
}

func writeBenchmarkFrame(conn net.Conn, frame []byte) error {
	for len(frame) > 0 {
		n, err := conn.Write(frame)
		if err != nil {
			return err
		}
		frame = frame[n:]
	}
	return nil
}
