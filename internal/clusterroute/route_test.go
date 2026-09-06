package clusterroute

import (
	"testing"

	"yola/locate"

	"github.com/stretchr/testify/require"
)

func TestGateRouteRoundTrip(t *testing.T) {
	binding := locate.GateBinding{
		ServiceName: "game", UID: "player-a", BindingToken: "binding-a",
		GateID: "gate-a", GateEndpoint: "grpc://127.0.0.1:9000", ConnID: "conn-a",
	}
	require.Equal(t, binding, ToBinding(FromBinding(binding)))
	require.Equal(t, locate.GateBinding{}, ToBinding(nil))
}
