package pushbench

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestSocketReceiverRejectsMissingDuplicateAndWrongTableMessages(t *testing.T) {
	for _, test := range []struct {
		name    string
		message proto.Message
	}{
		{name: "missing", message: cadencePayload(0, 3, 0)},
		{name: "duplicate", message: cadencePayload(0, 1, 0)},
		{name: "wrong_table", message: cadencePayload(1, 2, 0)},
		{name: "short_payload", message: wrapperspb.Bytes(make([]byte, 8))},
	} {
		t.Run(test.name, func(t *testing.T) {
			receiver := &socketReceiver{origin: time.Now(), measurements: newMeasurements(t)}
			first, err := proto.Marshal(cadencePayload(0, 1, 0))
			require.NoError(t, err)
			receiver.receive(first)
			body, err := proto.Marshal(test.message)
			require.NoError(t, err)
			receiver.receive(body)
			require.Error(t, receiver.err)
			require.Equal(t, uint64(1), receiver.received)
		})
	}
}

func TestSocketReceiverExcludesWarmupFromSequence(t *testing.T) {
	receiver := &socketReceiver{origin: time.Now(), measurements: newMeasurements(t)}
	for sequence := range uint64(3) {
		body, err := proto.Marshal(cadencePayload(0, sequence, 0))
		require.NoError(t, err)
		receiver.receive(body)
	}
	require.NoError(t, receiver.err)
	require.Equal(t, uint64(1), receiver.warmups.Load())
	require.Equal(t, uint64(2), receiver.received)
}
