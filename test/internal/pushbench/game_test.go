package pushbench

import (
	"errors"
	"testing"

	"yola/api/protocol/v1"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

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
