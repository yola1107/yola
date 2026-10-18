package node

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health/grpc_health_v1"
)

func TestResolveOptionsDefaultsAndZeroHandlerTimeout(t *testing.T) {
	o, err := resolveOptions(Registration(&countingRegistrar{}), HandlerTimeout(0))

	require.NoError(t, err)
	require.Equal(t, 3*time.Second, o.pushTimeout)
	require.Equal(t, 3*time.Second, o.cleanupTimeout)
	require.Equal(t, 10*time.Second, o.shutdownTimeout)
}

func TestConstructionRequiresRegistration(t *testing.T) {
	_, err := NewServer()
	require.EqualError(t, err, "node: registration is required")

	_, err = NewServer(Registration(nil))
	require.EqualError(t, err, "node: registration is required")
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

	o, err := resolveOptions(Registration(&countingRegistrar{}), ClientTLS(config))
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

			_, port, err := net.SplitHostPort(lis.Addr().String())
			require.NoError(t, err)
			opts := []Option{Listener(lis), Address("0.0.0.0:" + port), AdvertiseHost("192.168.1.10")}
			if test.secure {
				opts = append(opts, ServerTLS(testServerTLSConfig(t)))
			}
			registrar := newLifecycleRegistrar()
			opts = append(opts, Registration(registrar))
			server := newTestServer(t, opts...)
			startTestNode(testNodeContext(), t, server)
			require.Equal(t, []string{test.scheme + "://192.168.1.10:" + port}, registrar.instance().Endpoints)
		})
	}
}

func TestAdvertiseHostRejectsUnreachableAdvertisement(t *testing.T) {
	_, err := NewServer(Registration(&countingRegistrar{}), Address("127.0.0.1:9001"), AdvertiseHost("192.168.1.10"))
	require.EqualError(t, err, `node: loopback listen host "127.0.0.1" cannot advertise non-loopback host "192.168.1.10"`)
}

func TestConstructionRejectsInvalidConfiguration(t *testing.T) {
	for _, test := range []struct {
		name   string
		option Option
		want   string
	}{
		{name: "push timeout", option: PushTimeout(0), want: "node: push timeout must be positive"},
		{name: "cleanup timeout", option: CleanupTimeout(0), want: "node: cleanup timeout must be positive"},
		{name: "shutdown timeout", option: ShutdownTimeout(0), want: "node: shutdown timeout must be positive"},
		{name: "client TLS", option: ClientTLS(nil), want: "node: gRPC TLS config is required"},
		{name: "server TLS", option: ServerTLS(nil), want: "node: TLS config is required"},
		{
			name:   "server certificate",
			option: ServerTLS(&tls.Config{MinVersion: tls.VersionTLS12}),
			want:   "node: TLS certificate is required",
		},
		{name: "handler timeout", option: HandlerTimeout(-time.Second), want: "node: handler timeout cannot be negative"},
		{name: "listener", option: Listener(nil), want: "node: listener is required"},
		{name: "stateful store", option: Stateful(nil), want: "node: stateful store is required"},
		{
			name:   "typed nil stateful store",
			option: Stateful((*memoryNodeStore)(nil)),
			want:   "node: stateful store is required",
		},
		{name: "gate directory", option: GateDirectory(nil), want: "node: gate directory is required"},
		{
			name:   "typed nil gate directory",
			option: GateDirectory((*fixedGateDirectory)(nil)),
			want:   "node: gate directory is required",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, err := NewServer(Registration(&countingRegistrar{}), test.option)
			require.EqualError(t, err, test.want)
			require.Nil(t, server)
		})
	}
}

func TestServerAcceptsTLSConnection(t *testing.T) {
	serverTLS, clientTLS := testTLSConfigs(t)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := newTestServer(t, Listener(lis), ServerTLS(serverTLS), func(*options) error {
		// 在 Option 应用后、服务构造前修改原配置，验证证书来自副本。
		serverTLS.Certificates = nil
		return nil
	})
	ctx := testNodeContext()

	done := make(chan error, 1)
	go func() { done <- server.Start(ctx) }()
	t.Cleanup(func() {
		require.NoError(t, server.Stop(context.Background()))
		require.NoError(t, <-done)
	})

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(credentials.NewTLS(clientTLS)))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	requestCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	reply, err := grpc_health_v1.NewHealthClient(conn).Check(
		requestCtx, &grpc_health_v1.HealthCheckRequest{}, grpc.WaitForReady(true),
	)
	require.NoError(t, err)
	require.Equal(t, grpc_health_v1.HealthCheckResponse_SERVING, reply.Status)
}
