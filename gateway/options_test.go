package gateway

import (
	"crypto/tls"
	"errors"
	"net"
	"net/url"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestResolveOptionsDefaults(t *testing.T) {
	o, err := resolveOptions(
		Auth(testAuthenticator{}),
		Locator(pingLocator{}),
		Discovery(staticDiscovery{}),
	)

	require.NoError(t, err)
	require.Equal(t, 3*time.Second, o.rpcTimeout)
	require.Equal(t, 15*time.Second, o.authTimeout)
	require.Equal(t, 60*time.Second, o.leaseTTL)
	require.Equal(t, min(8, max(1, runtime.GOMAXPROCS(0))), o.broadcastWorkers)
	require.Equal(t, 256, o.broadcastQueueCapacity)
}

func TestResolveOptionsPropagatesOptionError(t *testing.T) {
	want := errors.New("option failed")

	_, err := resolveOptions(func(*options) error { return want })

	require.ErrorIs(t, err, want)
}

func TestResolveOptionsRejectsEndpointTLSMismatch(t *testing.T) {
	base := []Option{Auth(testAuthenticator{}), Locator(pingLocator{}), Discovery(staticDiscovery{})}
	tests := []struct {
		name string
		opts []Option
		want string
	}{
		{
			name: "endpoint without host",
			opts: []Option{Endpoint(&url.URL{Scheme: "grpc"})},
			want: "gateway: gRPC endpoint host is required",
		},
		{
			name: "secure endpoint without TLS",
			opts: []Option{Endpoint(&url.URL{Scheme: "grpcs", Host: "127.0.0.1:9010"})},
			want: "gateway: gRPC endpoint must use grpc:// with a host",
		},
		{
			name: "insecure endpoint with TLS",
			opts: []Option{
				Endpoint(&url.URL{Scheme: "grpc", Host: "127.0.0.1:9010"}),
				ServerTLS(testServerTLSConfig(t)),
			},
			want: "gateway: gRPC endpoint must use grpcs:// with a host",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := resolveOptions(append(base, test.opts...)...)

			require.EqualError(t, err, test.want)
		})
	}
}

func TestResolveOptionsRequiresDependencies(t *testing.T) {
	tests := []struct {
		name string
		opts []Option
		want string
	}{
		{
			name: "authenticator",
			opts: []Option{Locator(pingLocator{}), Discovery(staticDiscovery{})},
			want: "gateway: authenticator is required",
		},
		{
			name: "locator",
			opts: []Option{Auth(testAuthenticator{}), Discovery(staticDiscovery{})},
			want: "gateway: locator is required",
		},
		{
			name: "discovery",
			opts: []Option{Auth(testAuthenticator{}), Locator(pingLocator{})},
			want: "gateway: discovery is required",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := resolveOptions(test.opts...)

			require.EqualError(t, err, test.want)
		})
	}
}

func TestResolveOptionsClonesMutableConfiguration(t *testing.T) {
	endpoint := &url.URL{Scheme: "grpc", Host: "127.0.0.1:9010"}
	clientTLS := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: "node.internal"}

	o, err := resolveOptions(
		Auth(testAuthenticator{}),
		Locator(pingLocator{}),
		Discovery(staticDiscovery{}),
		Endpoint(endpoint),
		ClientTLS(clientTLS),
	)
	require.NoError(t, err)

	endpoint.Host = "mutated:9010"
	clientTLS.ServerName = "mutated.internal"
	require.Equal(t, "127.0.0.1:9010", o.endpoint.Host)
	require.Equal(t, "node.internal", o.clientTLS.ServerName)
	require.NotSame(t, endpoint, o.endpoint)
	require.NotSame(t, clientTLS, o.clientTLS)
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

func TestAdvertiseHostRejectsLoopbackListen(t *testing.T) {
	_, err := NewServer(
		Auth(testAuthenticator{}),
		Locator(pingLocator{}),
		Discovery(staticDiscovery{}),
		Address("127.0.0.1:9010"),
		AdvertiseHost("192.168.1.10"),
	)
	require.Error(t, err)
}

func TestServerTLSRequiresCertificate(t *testing.T) {
	_, err := NewServer(
		Auth(testAuthenticator{}),
		Locator(pingLocator{}),
		Discovery(staticDiscovery{}),
		ServerTLS(&tls.Config{MinVersion: tls.VersionTLS12}),
	)
	require.EqualError(t, err, "gateway: TLS certificate is required")
}

func TestGatewayConstructionReportsConfigurationCause(t *testing.T) {
	store := testLocator(t)
	tests := []struct {
		name string
		opts []Option
		want string
	}{
		{
			name: "authenticator",
			opts: []Option{Locator(store), Discovery(staticDiscovery{})},
			want: "gateway: authenticator is required",
		},
		{
			name: "locator",
			opts: []Option{Auth(testAuthenticator{}), Discovery(staticDiscovery{})},
			want: "gateway: locator is required",
		},
		{
			name: "discovery",
			opts: []Option{Auth(testAuthenticator{}), Locator(store)},
			want: "gateway: discovery is required",
		},
		{
			name: "zero lease",
			opts: []Option{Auth(testAuthenticator{}), Locator(store), Discovery(staticDiscovery{}), LeaseTTL(0)},
			want: "gateway: lease TTL must be at least one millisecond",
		},
		{
			name: "short lease",
			opts: []Option{
				Auth(testAuthenticator{}),
				Locator(store),
				Discovery(staticDiscovery{}),
				LeaseTTL(time.Microsecond),
			},
			want: "gateway: lease TTL must be at least one millisecond",
		},
		{
			name: "broadcast workers",
			opts: []Option{BroadcastWorkers(0)},
			want: "gateway: broadcast workers must be positive",
		},
		{
			name: "broadcast queue",
			opts: []Option{BroadcastQueueCapacity(0)},
			want: "gateway: broadcast queue capacity must be positive",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewServer(test.opts...)
			require.EqualError(t, err, test.want)
		})
	}
}
