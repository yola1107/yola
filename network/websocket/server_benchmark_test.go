package websocket

import (
	"fmt"
	"io"
	"log/slog"
	"testing"

	"yola/api/protocol/v1"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
)

func BenchmarkWebSocketServer(b *testing.B) {
	logger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.Cleanup(func() { slog.SetDefault(logger) })

	for _, payloadSize := range []int{32, 4000} {
		b.Run(fmt.Sprintf("request/payload=%d", payloadSize), func(b *testing.B) {
			conn := newWebSocketBenchmarkServer(b)
			frame, err := proto.Marshal(&v1.Proto{
				Op:   v1.OpRequest,
				Cmd:  1,
				Body: make([]byte, payloadSize),
			})
			if err != nil {
				b.Fatal(err)
			}
			b.SetBytes(int64(len(frame)))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if err = conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
					b.Fatal(err)
				}
				if _, _, err = conn.ReadMessage(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func newWebSocketBenchmarkServer(b *testing.B) *websocket.Conn {
	b.Helper()
	s := newWebSocketServer(b, websocketTestHandler{}, Address("127.0.0.1:0"))
	endpoint := serveWebSocketTestServer(b, s)
	conn := dialWebSocketTest(b, endpoint)
	b.Cleanup(func() { _ = conn.Close() })
	auth, err := proto.Marshal(&v1.Proto{Op: v1.OpAuth})
	if err != nil {
		b.Fatal(err)
	}
	if err = conn.WriteMessage(websocket.BinaryMessage, auth); err != nil {
		b.Fatal(err)
	}
	if _, _, err = conn.ReadMessage(); err != nil {
		b.Fatal(err)
	}
	return conn
}
