package network

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCarrierNormalizesKeys(t *testing.T) {
	c := headerCarrier{}
	c.Add("Trace-ID", "first")
	c.Add("trace-id", "second")
	require.Equal(t, "first", c.Get("trace-id"))
	c.Add("TRACE-ID", "third")
	require.Equal(t, []string{"first", "second", "third"}, c.Values("trace-id"))
}

func TestConnectionIDKey(t *testing.T) {
	tr := NewTransport(KindTCP, "tcp://127.0.0.1:1", "127.0.0.1", "connection-1")
	require.Equal(t, "connection-1", tr.RequestHeader().Get("conn_id"))
}
