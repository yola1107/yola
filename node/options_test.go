package node

import (
	"crypto/tls"
	"errors"
	"net"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestResolveOptionsDefaultsAndZeroHandlerTimeout(t *testing.T) {
	o, err := resolveOptions(HandlerTimeout(0))

	require.NoError(t, err)
	require.Equal(t, 3*time.Second, o.pushTimeout)
	require.Equal(t, 3*time.Second, o.cleanupTimeout)
}

func TestResolveOptionsPropagatesOptionError(t *testing.T) {
	want := errors.New("option failed")

	_, err := resolveOptions(func(*options) error { return want })

	require.ErrorIs(t, err, want)
}

func TestResolveOptionsRejectsEndpointTLSMismatch(t *testing.T) {
	tests := []struct {
		name string
		opts []Option
		want string
	}{
		{
			name: "endpoint without host",
			opts: []Option{Endpoint(&url.URL{Scheme: "grpc"})},
			want: "node: gRPC endpoint host is required",
		},
		{
			name: "secure endpoint without TLS",
			opts: []Option{Endpoint(&url.URL{Scheme: "grpcs", Host: "127.0.0.1:9010"})},
			want: "node: gRPC endpoint must use grpc:// with a host",
		},
		{
			name: "insecure endpoint with TLS",
			opts: []Option{
				Endpoint(&url.URL{Scheme: "grpc", Host: "127.0.0.1:9010"}),
				ServerTLS(testServerTLSConfig(t)),
			},
			want: "node: gRPC endpoint must use grpcs:// with a host",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := resolveOptions(test.opts...)

			require.EqualError(t, err, test.want)
		})
	}
}

func TestResolveOptionsClonesClientTLS(t *testing.T) {
	config := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: "gateway.internal"}

	o, err := resolveOptions(ClientTLS(config))
	require.NoError(t, err)
	config.ServerName = "mutated.internal"

	require.NotSame(t, config, o.clientTLS)
	require.Equal(t, "gateway.internal", o.clientTLS.ServerName)
}

func TestAdvertiseHostOptionPublishesEndpoint(t *testing.T) {
	for _, test := range []struct {
		name   string
		scheme string
		secure bool
	}{
		{name: "insecure", scheme: "grpc"},
		{name: "secure", scheme: "grpcs", secure: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			lis, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, lis.Close()) })
			_, port, err := net.SplitHostPort(lis.Addr().String())
			require.NoError(t, err)
			opts := []Option{Listener(lis), Address("0.0.0.0:" + port), AdvertiseHost("192.168.1.10")}
			if test.secure {
				opts = append(opts, ServerTLS(testServerTLSConfig(t)))
			}
			endpoint, err := newTestServer(t, opts...).Endpoint()
			require.NoError(t, err)
			require.Equal(t, test.scheme+"://192.168.1.10:"+port, endpoint.String())
		})
	}
}

func TestAdvertiseHostRejectsUnreachableAdvertisement(t *testing.T) {
	_, err := NewServer(Address("127.0.0.1:9001"), AdvertiseHost("192.168.1.10"))
	require.Error(t, err)
}

func TestConstructionRejectsInvalidConfiguration(t *testing.T) {
	for _, test := range []struct {
		name   string
		option Option
		want   string
	}{
		{name: "push timeout", option: PushTimeout(0), want: "node: push timeout must be positive"},
		{name: "cleanup timeout", option: CleanupTimeout(0), want: "node: cleanup timeout must be positive"},
		{name: "client TLS", option: ClientTLS(nil), want: "node: gRPC TLS config is required"},
		{name: "server TLS", option: ServerTLS(nil), want: "node: TLS config is required"},
		{
			name:   "server certificate",
			option: ServerTLS(&tls.Config{MinVersion: tls.VersionTLS12}),
			want:   "node: TLS certificate is required",
		},
		{name: "handler timeout", option: HandlerTimeout(-time.Second), want: "node: handler timeout cannot be negative"},
		{name: "listener", option: Listener(nil), want: "node: listener is required"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewServer(test.option)
			require.EqualError(t, err, test.want)
		})
	}
}
