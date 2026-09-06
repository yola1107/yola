package grpcendpoint

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAdvertise(t *testing.T) {
	for _, test := range []struct {
		name   string
		secure bool
		want   string
	}{
		{name: "insecure", want: "grpc://192.0.2.1:9010"},
		{name: "secure", secure: true, want: "grpcs://192.0.2.1:9010"},
	} {
		t.Run(test.name, func(t *testing.T) {
			endpoint, err := Advertise("0.0.0.0:9010", "192.0.2.1", test.secure)
			require.NoError(t, err)
			require.Equal(t, test.want, endpoint.String())
		})
	}
}

func TestAdvertiseRejectsInvalidAddress(t *testing.T) {
	_, err := Advertise("invalid", "192.0.2.1", false)
	require.ErrorContains(t, err, "invalid listen address")
	_, err = Advertise("0.0.0.0:0", "192.0.2.1", false)
	require.EqualError(t, err, `invalid listen port "0"`)
	_, err = Advertise("127.0.0.1:9010", "192.0.2.1", false)
	require.EqualError(t, err, `loopback listen host "127.0.0.1" cannot advertise non-loopback host "192.0.2.1"`)
}

func TestValidate(t *testing.T) {
	require.NoError(t, Validate(&url.URL{Scheme: "grpc", Host: "127.0.0.1:9010"}, false))
	require.NoError(t, Validate(&url.URL{Scheme: "grpcs", Host: "127.0.0.1:9010"}, true))
	require.ErrorIs(t, Validate(nil, false), ErrInvalidEndpoint)
	require.ErrorIs(t, Validate(&url.URL{Scheme: "http", Host: "127.0.0.1:9010"}, false), ErrInvalidEndpoint)
	require.EqualError(t,
		Validate(&url.URL{Scheme: "grpc", Host: "127.0.0.1:9010"}, true),
		"gRPC endpoint must use grpcs:// with a host",
	)
}

func TestHostRejectsEndpointComponentsOutsideHostPort(t *testing.T) {
	for _, raw := range []string{
		"grpc://localhost",
		"grpc://localhost:0",
		"grpc://localhost:not-a-port",
		"grpc://host%25zone:9010",
		"grpc://user@localhost:9010",
		"grpc://localhost:9010/path",
		"grpc://localhost:9010?query=value",
		"grpc://localhost:9010#fragment",
	} {
		t.Run(raw, func(t *testing.T) {
			_, err := Host(raw, false)
			require.ErrorIs(t, err, ErrInvalidEndpoint)
		})
	}
}

func TestHostUsesConfiguredTransport(t *testing.T) {
	host, err := Host("grpc://[::1]:9010", false)
	require.NoError(t, err)
	require.Equal(t, "[::1]:9010", host)
	host, err = Host("grpc://[fe80::1%25eth0]:9010", false)
	require.NoError(t, err)
	require.Equal(t, "[fe80::1%eth0]:9010", host)

	_, err = Host("grpcs://127.0.0.1:9010", false)
	require.ErrorIs(t, err, ErrTransportSecurity)
}
