package node

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/url"
	"testing"
	"time"

	kgrpc "github.com/go-kratos/kratos/v3/transport/grpc"
	"github.com/stretchr/testify/require"
	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

func TestResolveOptionsDefaultsAndZeroHandlerTimeout(t *testing.T) {
	o, err := resolveOptions(HandlerTimeout(0))

	require.NoError(t, err)
	require.Equal(t, 3*time.Second, o.pushTimeout)
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

func TestResolveOptionsClonesServerTLS(t *testing.T) {
	serverTLS, clientTLS := testTLSConfigs(t)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	o, err := resolveOptions(
		Listener(lis),
		ServerTLS(serverTLS),
		func(*options) error {
			serverTLS.Certificates = nil
			return nil
		},
	)
	require.NoError(t, err)
	server := kgrpc.NewServer(append(o.grpcOptions, kgrpc.Listener(o.listener))...)
	done := make(chan error, 1)
	go func() { done <- server.Start(context.Background()) }()
	t.Cleanup(func() {
		require.NoError(t, server.Stop(context.Background()))
		require.NoError(t, <-done)
	})

	conn, err := grpcgo.NewClient(lis.Addr().String(), grpcgo.WithTransportCredentials(credentials.NewTLS(clientTLS)))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	reply, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{}, grpcgo.WaitForReady(true))
	require.NoError(t, err)
	require.Equal(t, healthpb.HealthCheckResponse_SERVING, reply.Status)
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
