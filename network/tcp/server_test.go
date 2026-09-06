package tcp

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"yola/api/protocol/v1"
	"yola/network"
)

type blockingListener struct {
	accepting chan struct{}
	release   chan struct{}
	once      atomic.Bool
}

func eventually(timeout time.Duration, condition func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return condition()
}

func newBlockingListener() *blockingListener {
	return &blockingListener{accepting: make(chan struct{}), release: make(chan struct{})}
}

func (l *blockingListener) Accept() (net.Conn, error) {
	if l.once.CompareAndSwap(false, true) {
		close(l.accepting)
	}
	<-l.release
	return nil, net.ErrClosed
}

func (*blockingListener) Close() error { return nil }

func (*blockingListener) Addr() net.Addr { return &net.TCPAddr{IP: net.ParseIP("127.0.0.1")} }

func (l *blockingListener) forceClose() { close(l.release) }

func TestServerStopRespectsContextWhileAcceptIsBlocked(t *testing.T) {
	lis := newBlockingListener()
	s := newTCPServer(t, tcpTestHandler{}, Listener(lis))
	started := make(chan error, 1)
	go func() { started <- s.Start(context.Background()) }()
	<-lis.accepting

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	stopped := make(chan error, 1)
	go func() { stopped <- s.Stop(ctx) }()
	select {
	case err := <-stopped:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Stop() error = %v, want context deadline exceeded", err)
		}
	case <-time.After(200 * time.Millisecond):
		lis.forceClose()
		<-started
		t.Fatal("Stop did not respect its context")
	}
	lis.forceClose()
	if err := <-started; err != nil {
		t.Fatal(err)
	}
}

func TestServerStopPrefersCompletedServe(t *testing.T) {
	for range 64 {
		s := NewServer()
		s.cancel = func() {}
		close(s.serveDone)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		if err := s.Stop(ctx); err != nil {
			t.Fatalf("Stop() error = %v, want nil after serving completed", err)
		}
	}
}

func TestServerStopClosesActiveConnection(t *testing.T) {
	s := newTCPServer(t, tcpTestHandler{}, Address("127.0.0.1:0"))
	if _, err := s.Endpoint(); err != nil {
		t.Fatalf("Endpoint() error = %v", err)
	}
	errCh := make(chan error, 1)
	go func() { errCh <- s.Start(context.Background()) }()
	if !eventually(time.Second, func() bool {
		s.lifecycleMu.Lock()
		defer s.lifecycleMu.Unlock()
		return s.cancel != nil
	}) {
		t.Fatal("server did not start")
	}
	conn := dialTCPTest(t, s.lis.Addr().String())
	if err := s.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("connection remained open after Stop")
	}
	if err := <-errCh; err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatalf("second Stop() error = %v", err)
	}
}

func TestEndpointUsesConfiguredListener(t *testing.T) {
	tests := []struct {
		name    string
		scheme  string
		options []ServerOption
	}{
		{name: "TCP", scheme: "tcp"},
		{name: "TLS", scheme: "tcps", options: []ServerOption{TLSConfig(&tls.Config{
			MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{}},
		})}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lis, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			options := append([]ServerOption{Listener(lis)}, test.options...)
			s := NewServer(options...)
			endpoint, err := s.Endpoint()
			if err != nil {
				t.Fatalf("Endpoint() error = %v", err)
			}
			want := test.scheme + "://" + lis.Addr().String()
			if endpoint.String() != want {
				t.Fatalf("Endpoint() = %s, want %s", endpoint, want)
			}
			if s.lis != lis {
				t.Fatal("Endpoint replaced the configured listener")
			}
			if err := s.Stop(context.Background()); err != nil {
				t.Fatalf("Stop() error = %v", err)
			}
		})
	}
}

func TestServerCanPrepareAfterListenFailure(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := occupied.Addr().String()
	s := newTCPServer(t, tcpTestHandler{}, Address(address))
	if err = s.BeforeStart(context.Background()); err == nil {
		t.Fatal("BeforeStart succeeded while address was occupied")
	}
	if err = occupied.Close(); err != nil {
		t.Fatal(err)
	}
	if err = s.BeforeStart(context.Background()); err != nil {
		t.Fatalf("BeforeStart after releasing address: %v", err)
	}
	if err = s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	rebound, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("listener remained open after Stop: %v", err)
	}
	_ = rebound.Close()
}

func TestEndpointRejectsInvalidConfigurationBeforeBinding(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := probe.Addr().String()
	if err = probe.Close(); err != nil {
		t.Fatal(err)
	}
	server := NewServer(Address(address), Codec(nil))
	if _, err = server.Endpoint(); err == nil || err.Error() != "tcp: codec is required" {
		t.Fatalf("Endpoint() error = %v, want invalid codec", err)
	}

	rebound, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("invalid configuration bound the listener: %v", err)
	}
	_ = rebound.Close()
}

func TestStoppedServerDoesNotOpenEndpoint(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := probe.Addr().String()
	if err = probe.Close(); err != nil {
		t.Fatal(err)
	}
	s := NewServer(Address(address))
	if err = s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	if _, err = s.Endpoint(); err == nil || err.Error() != "tcp: server is stopped" {
		t.Fatalf("Endpoint() error = %v, want stopped", err)
	}
	rebound, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	if err = rebound.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestServerRejectsInvalidConfiguration(t *testing.T) {
	for _, test := range []struct {
		name   string
		server *Server
		want   string
	}{
		{
			name:   "missing handler",
			server: NewServer(),
			want:   "tcp: connection handler is required",
		},
		{
			name:   "nil codec",
			server: newTCPServer(t, tcpTestHandler{}, Codec(nil)),
			want:   "tcp: codec is required",
		},
		{
			name:   "handler timeout",
			server: newTCPServer(t, tcpTestHandler{}, Timeout(-1)),
			want:   "tcp: handler timeout cannot be negative",
		},
		{
			name:   "connection limit",
			server: newTCPServer(t, tcpTestHandler{}, MaxConnLimit(0)),
			want:   "tcp: connection limit must be positive",
		},
		{
			name:   "per-IP limit",
			server: newTCPServer(t, tcpTestHandler{}, MaxConnPerIP(0)),
			want:   "tcp: per-IP connection limit must be positive",
		},
		{
			name:   "handshake timeout",
			server: newTCPServer(t, tcpTestHandler{}, HandshakeTimeout(0)),
			want:   "tcp: handshake timeout must be positive",
		},
		{
			name:   "heartbeat timeout",
			server: newTCPServer(t, tcpTestHandler{}, HeartbeatTimeout(0)),
			want:   "tcp: heartbeat timeout must be positive",
		},
		{
			name:   "write timeout",
			server: newTCPServer(t, tcpTestHandler{}, WriteTimeout(0)),
			want:   "tcp: write timeout must be positive",
		},
		{
			name: "TLS verification",
			server: newTCPServer(
				t,
				tcpTestHandler{},
				TLSConfig(&tls.Config{InsecureSkipVerify: true}),
			),
			want: "tcp: TLS certificate verification must be enabled",
		},
		{
			name: "TLS certificate",
			server: newTCPServer(
				t,
				tcpTestHandler{},
				TLSConfig(&tls.Config{MinVersion: tls.VersionTLS12}),
			),
			want: "tcp: TLS certificate is required",
		},
		{
			name: "plain endpoint with TLS",
			server: newTCPServer(
				t,
				tcpTestHandler{},
				Endpoint(&url.URL{Scheme: "tcp", Host: "example.com:443"}),
				TLSConfig(&tls.Config{Certificates: []tls.Certificate{{}}}),
			),
			want: "tcp: endpoint must use tcps:// with a host",
		},
		{
			name: "secure endpoint without TLS",
			server: newTCPServer(
				t,
				tcpTestHandler{},
				Endpoint(&url.URL{Scheme: "tcps", Host: "example.com:443"}),
			),
			want: "tcp: endpoint must use tcp:// with a host",
		},
		{
			name:   "endpoint host",
			server: newTCPServer(t, tcpTestHandler{}, Endpoint(&url.URL{Scheme: "tcp"})),
			want:   "tcp: endpoint must use tcp:// with a host",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.server.BeforeStart(context.Background()); err == nil || err.Error() != test.want {
				t.Fatalf("BeforeStart() error = %v, want %q", err, test.want)
			}
			if err := test.server.Start(context.Background()); err == nil || err.Error() != test.want {
				t.Fatalf("Start() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestServerEndpointConfigIsCloned(t *testing.T) {
	configured := &url.URL{Scheme: "tcp", Host: "example.com:3101"}
	server := newTCPServer(t, tcpTestHandler{}, Address("127.0.0.1:0"), Endpoint(configured))
	t.Cleanup(func() { _ = server.Stop(context.Background()) })
	configured.Host = "changed.example.com:3101"
	if server.endpoint == configured || server.endpoint.Host != "example.com:3101" {
		t.Fatalf("server endpoint = %v", server.endpoint)
	}
	endpoint, err := server.Endpoint()
	if err != nil {
		t.Fatal(err)
	}
	endpoint.Host = "mutated.example.com:3101"
	endpoint, err = server.Endpoint()
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.Host != "example.com:3101" {
		t.Fatalf("Endpoint() retained caller mutation: %s", endpoint)
	}
}

func TestServerAdvertiseHostOverridesListenHost(t *testing.T) {
	server := newTCPServer(t, tcpTestHandler{}, Address("127.0.0.1:0"), AdvertiseHost("game.example.com"))
	t.Cleanup(func() { _ = server.Stop(context.Background()) })
	endpoint, err := server.Endpoint()
	if err != nil {
		t.Fatal(err)
	}
	host, _, err := net.SplitHostPort(endpoint.Host)
	if err != nil {
		t.Fatal(err)
	}
	if host != "game.example.com" {
		t.Fatalf("Endpoint() host = %q, want game.example.com", host)
	}
}

func TestServerTopLevelTLSConfigIsCloned(t *testing.T) {
	config := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{}}}
	server := NewServer(TLSConfig(config))
	config.MinVersion = tls.VersionTLS13
	config.Certificates = nil
	if server.config.tls == config {
		t.Fatal("server retained caller TLS config")
	}
	if server.config.tls.MinVersion != tls.VersionTLS12 || len(server.config.tls.Certificates) != 1 {
		t.Fatal("server TLS config changed with caller config")
	}
}

func TestServerSetHandlerAllowsDeferredInjection(t *testing.T) {
	s := NewServer(Address("127.0.0.1:0"))
	if err := s.SetHandler(nil); err == nil {
		t.Fatal("SetHandler succeeded with nil handler")
	}
	if err := s.SetHandler(tcpTestHandler{}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetHandler(tcpTestHandler{}); err == nil || err.Error() != "tcp: connection handler is already set" {
		t.Fatalf("second SetHandler() error = %v, want already set", err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Start(context.Background()) }()
	endpoint, err := s.Endpoint()
	if err != nil {
		t.Fatal(err)
	}
	conn := dialTCPTest(t, endpoint.Host)
	_ = conn.Close()
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := s.SetHandler(tcpTestHandler{}); err == nil {
		t.Fatal("SetHandler succeeded after Start")
	}
}

func TestServerEnforcesConnectionLimits(t *testing.T) {
	for _, test := range []struct {
		name    string
		options []ServerOption
	}{
		{name: "total", options: []ServerOption{MaxConnLimit(1)}},
		{name: "per IP", options: []ServerOption{MaxConnLimit(2), MaxConnPerIP(1)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := &lifecycleHandler{opened: make(chan struct{}, 2)}
			options := append([]ServerOption{Address("127.0.0.1:0")}, test.options...)
			server := newTCPServer(t, h, options...)
			endpoint := serveTCPTestServer(t, server)
			first := dialTCPTest(t, endpoint)
			waitTCPValue(t, h.opened)
			second := dialTCPTest(t, endpoint)
			_ = second.SetReadDeadline(time.Now().Add(time.Second))
			if _, err := second.Read(make([]byte, 1)); err == nil {
				t.Fatal("connection above limit remained open")
			}
			_ = second.Close()
			select {
			case <-h.opened:
				t.Fatal("connection above limit reached Open")
			case <-time.After(20 * time.Millisecond):
			}
			_ = first.Close()
			if !eventually(time.Second, func() bool { return tcpServerConnectionCount(server) == 0 }) {
				t.Fatal("connection slot was not released")
			}
			third := dialTCPTest(t, endpoint)
			defer third.Close()
			waitTCPValue(t, h.opened)
		})
	}
}

type lifecycleHandler struct {
	openErr error
	opened  chan struct{}
}

func (h *lifecycleHandler) Open(context.Context, network.Connection) error {
	h.opened <- struct{}{}
	return h.openErr
}

func (*lifecycleHandler) Handle(_ context.Context, _ network.Connection, message *v1.Proto) (*v1.Proto, error) {
	return message, nil
}

func (*lifecycleHandler) Close(context.Context, network.Connection) {}
