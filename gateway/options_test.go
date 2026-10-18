package gateway

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/url"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health/grpc_health_v1"
)

func TestResolveOptionsDefaults(t *testing.T) {
	o, err := resolveOptions(
		Auth(testAuthenticator{}),
		Locator(pingLocator{}),
		Discovery(staticDiscovery{}),
	)

	require.NoError(t, err)
	require.Equal(t, 3*time.Second, o.rpcTimeout)
	require.Equal(t, 3*time.Second, o.connectTimeout)
	require.Equal(t, 3*time.Second, o.leaseTimeout)
	require.Equal(t, 3*time.Second, o.cleanupTimeout)
	require.Equal(t, 10*time.Second, o.shutdownTimeout)
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
			t.Cleanup(func() { _ = lis.Close() })
			_, port, err := net.SplitHostPort(lis.Addr().String())
			require.NoError(t, err)
			opts := []Option{Listener(lis), Address("0.0.0.0:" + port), AdvertiseHost("192.168.1.10")}
			if test.secure {
				opts = append(opts, ServerTLS(testServerTLSConfig(t)))
			}
			store := testLocator(t)
			gate := newTestServer(t, append(opts, Locator(store))...)
			initTestGateway(t, gate)
			conn := newTestConnection("conn-advertised-endpoint")
			require.NoError(t, gate.Open(t.Context(), conn))
			_, err = gate.Handle(t.Context(), conn, authMessage(t))
			require.NoError(t, err)
			lease, err := store.LocateGate(t.Context(), "game", "player-a")
			require.NoError(t, err)
			require.Equal(t, test.scheme+"://192.168.1.10:"+port, lease.Binding.GateEndpoint)
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
	store := pingLocator{}
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

func TestClientTLSRejectsNilConfig(t *testing.T) {
	var nilOptions options
	require.EqualError(t, ClientTLS(nil)(&nilOptions), "gateway: gRPC TLS config is required")
}

func TestTimeoutOptionsRejectNonPositive(t *testing.T) {
	for _, test := range []struct {
		name   string
		option func(time.Duration) Option
	}{
		{name: "connect", option: ConnectTimeout},
		{name: "lease", option: LeaseTimeout},
		{name: "cleanup", option: CleanupTimeout},
		{name: "shutdown", option: ShutdownTimeout},
	} {
		for _, timeout := range []time.Duration{0, -time.Second} {
			t.Run(test.name+"/"+timeout.String(), func(t *testing.T) {
				_, err := resolveOptions(Auth(testAuthenticator{}), Locator(pingLocator{}), Discovery(staticDiscovery{}), test.option(timeout))
				require.Error(t, err)
			})
		}
	}
}

func TestServerAcceptsTLSConnection(t *testing.T) {
	serverTLS, clientTLS := testTLSConfigs(t)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	endpoint := &url.URL{Scheme: "grpcs", Host: lis.Addr().String()}
	server := newTestServer(t, Listener(lis), Endpoint(endpoint), ServerTLS(serverTLS))
	startTestApp(t, server)

	conn, err := grpc.NewClient(endpoint.Host, grpc.WithTransportCredentials(credentials.NewTLS(clientTLS)))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	reply, err := grpc_health_v1.NewHealthClient(conn).Check(ctx, &grpc_health_v1.HealthCheckRequest{}, grpc.WaitForReady(true))
	require.NoError(t, err)
	require.Equal(t, grpc_health_v1.HealthCheckResponse_SERVING, reply.Status)
}

func TestGatewayConstructionReportsSetHandlerFailure(t *testing.T) {
	handlerErr := errors.New("handler rejected")
	_, err := NewServer(
		Auth(testAuthenticator{}),
		Locator(pingLocator{}),
		Discovery(staticDiscovery{}),
		Transport(&stubTransport{handlerErr: handlerErr}),
	)
	require.ErrorIs(t, err, handlerErr)
}
