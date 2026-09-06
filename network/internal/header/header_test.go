package header

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCarrierNormalizesKeys(t *testing.T) {
	c := Carrier{}
	c.Add("Trace-ID", "first")
	c.Add("trace-id", "second")
	require.Equal(t, "first", c.Get("trace-id"))
	c.Add("TRACE-ID", "third")
	require.Equal(t, []string{"first", "second", "third"}, c.Values("trace-id"))
}

func TestConnectionIDKey(t *testing.T) {
	require.Equal(t, "conn_id", ConnectionIDKey)
}
