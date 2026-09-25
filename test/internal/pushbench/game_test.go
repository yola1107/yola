package pushbench

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"yola/api/protocol/v1"
	"yola/network/websocket"
	"yola/node"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestGameRequestTimeoutsAcrossWebSocketAndGRPC(t *testing.T) {
	client := GameRedis(t)
	for _, layer := range []string{"transport", "forward", "node"} {
		t.Run(layer, func(t *testing.T) {
			timeouts := RequestTimeouts{Transport: time.Second, Forward: time.Second, Node: time.Second}
			limit := 250 * time.Millisecond
			switch layer {
			case "transport":
				timeouts.Transport = limit
			case "forward":
				timeouts.Forward = limit
			case "node":
				timeouts.Node = limit
			}
			budgets, finished := make(chan time.Duration, 1), make(chan struct{})
			service := "budget-" + rand.Text()
			probe := StartGame(t, service, client, func(server *node.Server) {
				server.RegisterRawHandler(0, func(context.Context, []byte) ([]byte, error) { return nil, nil })
				server.RegisterRawHandler(1, func(ctx context.Context, _ []byte) ([]byte, error) {
					defer close(finished)
					deadline, _ := ctx.Deadline()
					budgets <- time.Until(deadline)
					<-ctx.Done()
					return nil, ctx.Err()
				})
			}, nil, 1, timeouts)
			connection, err := websocket.NewClient(t.Context(), websocket.WithEndpoint(probe.Endpoint),
				websocket.WithServiceName(service), websocket.WithToken("1"))
			require.NoError(t, err)
			t.Cleanup(connection.Close)
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			_, code, err := connection.Request(ctx, 0, new(emptypb.Empty))
			require.NoError(t, err)
			require.Zero(t, code)
			_, code, err = connection.Request(ctx, 1, new(emptypb.Empty))
			require.NoError(t, err, "client must receive an error response instead of timing out locally")
			require.Equal(t, int32(codes.DeadlineExceeded), code)
			select {
			case budget := <-budgets:
				require.Positive(t, budget)
				require.LessOrEqual(t, budget, limit)
			default:
				t.Fatal("request never reached the Node handler")
			}
			select {
			case <-finished:
			case <-ctx.Done():
				t.Fatal("Node handler did not observe cancellation")
			}
		})
	}
}

func TestGameDeliveryRejectsMissingDuplicateReorderedAndChangedMessages(t *testing.T) {
	for _, test := range []struct {
		name   string
		bodies []string
	}{
		{name: "missing", bodies: []string{"a"}},
		{name: "duplicate", bodies: []string{"a", "a", "b"}},
		{name: "reordered", bodies: []string{"b", "a"}},
		{name: "changed", bodies: []string{"a", "c"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			probe := &GameProbe{streams: make(map[string]*messageStream)}
			stream := probe.stream("1")
			writeMessageDigest(stream.sent, 1, []byte("a"))
			writeMessageDigest(stream.sent, 1, []byte("b"))
			stream.sentCount = 2
			for _, body := range test.bodies {
				probe.RecordPush(1, 1, []byte(body))
			}
			require.Error(t, probe.deliveryError(1))
		})
	}
}

func TestGameCodecKeepsPendingRequestsAndChecksErrorsAfterMeasurement(t *testing.T) {
	probe := &GameProbe{streams: make(map[string]*messageStream), measurements: newMeasurements(t)}
	rejected := errors.New("business response rejected")
	codec := probe.Codec(func(int32, []byte) error { return rejected })
	_, err := codec.Marshal(&v1.Proto{Op: v1.OpRequest, Seq: 1, Cmd: 1})
	require.NoError(t, err)
	require.Equal(t, int64(1), probe.requests.Load())
	probe.ReportMeasurements(t)
	body, err := proto.Marshal(&v1.Proto{Op: v1.OpResponse, Seq: 1, Cmd: 1})
	require.NoError(t, err)
	require.NoError(t, codec.Unmarshal(body, new(v1.Proto)))
	require.Zero(t, probe.requests.Load())
	require.ErrorIs(t, probe.Err(), rejected)
}

func TestGameCodecRejectsDuplicateResponses(t *testing.T) {
	probe := &GameProbe{streams: make(map[string]*messageStream)}
	codec := probe.Codec(nil)
	_, err := codec.Marshal(&v1.Proto{Op: v1.OpRequest, Seq: 1, Cmd: 1})
	require.NoError(t, err)
	body, err := proto.Marshal(&v1.Proto{Op: v1.OpResponse, Seq: 1, Cmd: 1})
	require.NoError(t, err)
	require.NoError(t, codec.Unmarshal(body, new(v1.Proto)))
	require.NoError(t, probe.Err())
	require.NoError(t, codec.Unmarshal(body, new(v1.Proto)))
	require.Error(t, probe.Err())
	require.Zero(t, probe.requests.Load())
}

func TestGameDeliveryRequiresEveryPlayerAndPreservesMessageBoundaries(t *testing.T) {
	probe := &GameProbe{streams: make(map[string]*messageStream)}
	require.Error(t, probe.deliveryError(1))
	stream := probe.stream("1")
	writeMessageDigest(stream.sent, 1, []byte("a"))
	writeMessageDigest(stream.sent, 2, []byte("bc"))
	stream.sentCount = 2
	probe.RecordPush(1, 1, []byte("a"))
	probe.RecordPush(1, 2, []byte("bc"))
	require.NoError(t, probe.deliveryError(1))
	require.Error(t, probe.deliveryError(2))
	stream.received.Reset()
	writeMessageDigest(stream.received, 1, []byte("ab"))
	writeMessageDigest(stream.received, 2, []byte("c"))
	require.Error(t, probe.deliveryError(1))
}
