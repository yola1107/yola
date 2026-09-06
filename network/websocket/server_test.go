package websocket

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yola/api/protocol/v1"
	"yola/network"

	"github.com/gorilla/websocket"
)

func serverChannel(server *Server, connID string) *Channel {
	server.lifecycleMu.Lock()
	defer server.lifecycleMu.Unlock()
	return server.channels[connID]
}

func TestServersUseIndependentMux(_ *testing.T) {
	NewServer(Path("/ws"))
	NewServer(Path("/ws"))
}

func TestEndpointUsesServerConfiguration(t *testing.T) {
	for _, test := range []struct {
		name    string
		scheme  string
		path    string
		options []ServerOption
	}{
		{name: "listener", scheme: "ws", path: "/"},
		{name: "path", scheme: "ws", path: "/ws", options: []ServerOption{Path("/ws")}},
		{name: "TLS", scheme: "wss", path: "/", options: []ServerOption{TLSConfig(&tls.Config{
			MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{}},
		})}},
	} {
		t.Run(test.name, func(t *testing.T) {
			lis, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			server := NewServer(append([]ServerOption{Listener(lis)}, test.options...)...)
			endpoint, err := server.Endpoint()
			if err != nil {
				t.Fatalf("Endpoint() error = %v", err)
			}
			want := test.scheme + "://" + lis.Addr().String() + test.path
			if endpoint.String() != want {
				t.Fatalf("Endpoint() = %s, want %s", endpoint, want)
			}
			if err := server.Stop(context.Background()); err != nil {
				t.Fatalf("Stop() error = %v", err)
			}
		})
	}
}

func TestServerConfiguresHTTPHeaderLimits(t *testing.T) {
	server := NewServer()
	if server.httpServer.ReadHeaderTimeout <= 0 || server.httpServer.MaxHeaderBytes <= 0 {
		t.Fatal("HTTP header limits are not configured")
	}
}

func TestServerTLSConfigIsCloned(t *testing.T) {
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

func TestServerAcceptsTLSConnection(t *testing.T) {
	certificateSource := httptest.NewTLSServer(http.NotFoundHandler())
	serverTLS := certificateSource.TLS.Clone()
	roots := x509.NewCertPool()
	roots.AddCert(certificateSource.Certificate())
	certificateSource.Close()

	server := newWebSocketServer(t, websocketTestHandler{}, Address("127.0.0.1:0"), TLSConfig(serverTLS))
	endpoint := serveWebSocketTestServer(t, server)

	dialer := websocket.Dialer{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}}
	conn, response, err := dialer.Dial(endpoint, nil)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
}

func TestServerCanPrepareAfterListenFailure(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := occupied.Addr().String()
	s := newWebSocketServer(t, websocketTestHandler{}, Address(address))
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
	server := NewServer(Address(address), Path("invalid"))
	if _, err = server.Endpoint(); err == nil || err.Error() != "websocket: invalid path" {
		t.Fatalf("Endpoint() error = %v, want invalid path", err)
	}

	rebound, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("invalid configuration bound the listener: %v", err)
	}
	_ = rebound.Close()
}

func TestServerRejectsInvalidConfiguration(t *testing.T) {
	for _, test := range []struct {
		name   string
		server *Server
		want   string
	}{
		{
			name:   "invalid path",
			server: newWebSocketServer(t, websocketTestHandler{}, Path("invalid")),
			want:   "websocket: invalid path",
		},
		{
			name:   "missing handler",
			server: NewServer(),
			want:   "websocket: connection handler is required",
		},
		{
			name:   "nil codec",
			server: newWebSocketServer(t, websocketTestHandler{}, Codec(nil)),
			want:   "websocket: codec is required",
		},
		{
			name:   "nil channel config",
			server: newWebSocketServer(t, websocketTestHandler{}, ServerChannelConfig(nil)),
			want:   "websocket: channel config is required",
		},
		{
			name:   "connection limit",
			server: newWebSocketServer(t, websocketTestHandler{}, MaxConnLimit(0)),
			want:   "websocket: connection limit must be positive",
		},
		{
			name:   "per-IP limit",
			server: newWebSocketServer(t, websocketTestHandler{}, MaxConnPerIP(0)),
			want:   "websocket: per-IP connection limit must be positive",
		},
		{
			name:   "handshake timeout",
			server: newWebSocketServer(t, websocketTestHandler{}, HandshakeTimeout(0)),
			want:   "websocket: handshake timeout must be positive",
		},
		{
			name:   "max header bytes",
			server: newWebSocketServer(t, websocketTestHandler{}, MaxHeaderBytes(0)),
			want:   "websocket: max header bytes must be positive",
		},
		{
			name:   "handler timeout",
			server: newWebSocketServer(t, websocketTestHandler{}, Timeout(-1)),
			want:   "websocket: handler timeout cannot be negative",
		},
		{
			name: "TLS verification",
			server: newWebSocketServer(
				t,
				websocketTestHandler{},
				TLSConfig(&tls.Config{InsecureSkipVerify: true}),
			),
			want: "websocket: TLS certificate verification must be enabled",
		},
		{
			name: "TLS certificate",
			server: newWebSocketServer(
				t,
				websocketTestHandler{},
				TLSConfig(&tls.Config{MinVersion: tls.VersionTLS12}),
			),
			want: "websocket: TLS certificate is required",
		},
		{
			name: "plain endpoint with TLS",
			server: newWebSocketServer(
				t,
				websocketTestHandler{},
				Endpoint(&url.URL{Scheme: "ws", Host: "example.com"}),
				TLSConfig(&tls.Config{Certificates: []tls.Certificate{{}}}),
			),
			want: "websocket: endpoint must use wss:// with a host",
		},
		{
			name: "secure endpoint without TLS",
			server: newWebSocketServer(
				t,
				websocketTestHandler{},
				Endpoint(&url.URL{Scheme: "wss", Host: "example.com"}),
			),
			want: "websocket: endpoint must use ws:// with a host",
		},
		{
			name:   "endpoint host",
			server: newWebSocketServer(t, websocketTestHandler{}, Endpoint(&url.URL{Scheme: "ws"})),
			want:   "websocket: endpoint must use ws:// with a host",
		},
		{
			name: "endpoint path",
			server: newWebSocketServer(
				t,
				websocketTestHandler{},
				Path("/ws"),
				Endpoint(&url.URL{Scheme: "ws", Host: "example.com", Path: "/other"}),
			),
			want: "websocket: endpoint path must match server path",
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
	configured := &url.URL{Scheme: "ws", Host: "example.com", Path: "/socket"}
	server := newWebSocketServer(t, websocketTestHandler{}, Address("127.0.0.1:0"), Path("/socket"), Endpoint(configured))
	t.Cleanup(func() { _ = server.Stop(context.Background()) })
	configured.Host = "changed.example.com"
	if server.endpoint == configured || server.endpoint.Host != "example.com" {
		t.Fatalf("server endpoint = %v", server.endpoint)
	}
	endpoint, err := server.Endpoint()
	if err != nil {
		t.Fatal(err)
	}
	endpoint.Host = "mutated.example.com"
	endpoint, err = server.Endpoint()
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.Host != "example.com" {
		t.Fatalf("Endpoint() retained caller mutation: %s", endpoint)
	}
}

func TestServerAdvertiseHostOverridesListenHost(t *testing.T) {
	server := newWebSocketServer(t, websocketTestHandler{}, Address("127.0.0.1:0"), AdvertiseHost("game.example.com"))
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

func TestServerSetHandlerAllowsDeferredInjection(t *testing.T) {
	s := NewServer(Address("127.0.0.1:0"))
	if err := s.SetHandler(nil); err == nil {
		t.Fatal("SetHandler succeeded with nil handler")
	}
	if err := s.SetHandler(websocketTestHandler{}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetHandler(websocketTestHandler{}); err == nil || err.Error() != "websocket: connection handler is already set" {
		t.Fatalf("second SetHandler() error = %v, want already set", err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Start(context.Background()) }()
	endpoint, err := s.Endpoint()
	if err != nil {
		t.Fatal(err)
	}
	conn := dialWebSocketTest(t, "ws://"+endpoint.Host+"/")
	_ = conn.Close()
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := s.SetHandler(websocketTestHandler{}); err == nil {
		t.Fatal("SetHandler succeeded after Start")
	}
}

type lifecycleHandler struct {
	opened chan network.Connection
	closed chan network.Connection
}

func (h *lifecycleHandler) Open(_ context.Context, conn network.Connection) error {
	h.opened <- conn
	return nil
}

func (*lifecycleHandler) Handle(_ context.Context, _ network.Connection, message *v1.Proto) (*v1.Proto, error) {
	return message, nil
}
func (h *lifecycleHandler) Close(_ context.Context, conn network.Connection) { h.closed <- conn }

type countingListener struct {
	net.Listener
	closes atomic.Int32
}

func (l *countingListener) Close() error {
	l.closes.Add(1)
	return l.Listener.Close()
}

type serveGateListener struct {
	*countingListener
	armed   atomic.Bool
	entered chan struct{}
	release <-chan struct{}
}

func (l *serveGateListener) Addr() net.Addr {
	if l.armed.CompareAndSwap(true, false) {
		close(l.entered)
		<-l.release
	}
	return l.Listener.Addr()
}

func TestServerStopClosesListenerBeforeServeTakesOwnership(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lis.Close() })
	release := make(chan struct{})
	releaseServe := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseServe)
	gated := &serveGateListener{
		countingListener: &countingListener{Listener: lis},
		entered:          make(chan struct{}),
		release:          release,
	}
	s := newWebSocketServer(t, websocketTestHandler{}, Listener(gated), Address(lis.Addr().String()))
	if _, err = s.Endpoint(); err != nil {
		t.Fatal(err)
	}
	gated.armed.Store(true)
	serverDone := make(chan error, 1)
	go func() { serverDone <- s.Start(context.Background()) }()
	waitWebSocketValue(t, gated.entered)

	if err = s.Stop(context.Background()); err != nil {
		t.Fatalf("Server.Stop() error = %v", err)
	}
	if closes := gated.closes.Load(); closes != 1 {
		t.Fatalf("Listener.Close calls before Serve = %d, want 1", closes)
	}
	releaseServe()
	if err = waitWebSocketValue(t, serverDone); err != nil {
		t.Fatalf("Server.Start() error = %v", err)
	}
}

type blockingCloseHandler struct {
	opened  chan struct{}
	closing chan struct{}
	release chan struct{}
}

func (h *blockingCloseHandler) Open(context.Context, network.Connection) error {
	close(h.opened)
	return nil
}

func (*blockingCloseHandler) Handle(_ context.Context, _ network.Connection, message *v1.Proto) (*v1.Proto, error) {
	return message, nil
}

func (h *blockingCloseHandler) Close(context.Context, network.Connection) {
	close(h.closing)
	<-h.release
}

func TestServerStopCanRetryAfterTimeout(t *testing.T) {
	h := &blockingCloseHandler{
		opened:  make(chan struct{}),
		closing: make(chan struct{}),
		release: make(chan struct{}),
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(h.release) }) }
	t.Cleanup(release)

	s := newWebSocketServer(t, h, Address("127.0.0.1:0"))
	endpoint, err := s.Endpoint()
	if err != nil {
		t.Fatalf("Server.Endpoint() error = %v", err)
	}
	serverDone := make(chan error, 1)
	go func() { serverDone <- s.Start(context.Background()) }()
	conn := dialWebSocketTest(t, endpoint.String())
	t.Cleanup(func() { _ = conn.Close() })
	waitWebSocketValue(t, h.opened)

	for attempt := 1; attempt <= 3; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		err = s.Stop(ctx)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Server.Stop() attempt %d error = %v, want %v", attempt, err, context.DeadlineExceeded)
		}
		if attempt == 1 {
			waitWebSocketValue(t, h.closing)
		}
	}

	release()
	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = s.Stop(stopCtx); err != nil {
		t.Errorf("Server.Stop() after releasing Close error = %v, want nil", err)
	}
	if err = waitWebSocketValue(t, serverDone); err != nil {
		t.Errorf("Server.Start() error = %v, want nil", err)
	}
}

func TestServerStopClosesActiveChannel(t *testing.T) {
	h := &lifecycleHandler{opened: make(chan network.Connection, 1), closed: make(chan network.Connection, 1)}
	s := newWebSocketServer(t, h, Address("127.0.0.1:0"))
	endpoint, err := s.Endpoint()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Start(context.Background()) }()
	conn := dialWebSocketTest(t, endpoint.String())
	ch := waitWebSocketValue(t, h.opened)
	stored := serverChannel(s, ch.ConnID())
	if stored == nil {
		t.Fatal("active channel was not stored")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := waitWebSocketValue(t, done); err != nil {
		t.Fatal(err)
	}
	if closed := waitWebSocketValue(t, h.closed); closed != ch {
		t.Fatalf("closed channel = %p, want %p", closed, ch)
	}
	stored = serverChannel(s, ch.ConnID())
	if count := serverConnectionCount(s); count != 0 || stored != nil {
		t.Fatalf("connection count after Stop = %d", count)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("client connection remained open after Stop")
	}
	_ = conn.Close()
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
			options := append([]ServerOption{Address("127.0.0.1:0")}, test.options...)
			s := newWebSocketServer(t, websocketTestHandler{}, options...)
			endpoint := serveWebSocketTestServer(t, s)
			first := dialWebSocketTest(t, endpoint)
			second, response, err := websocket.DefaultDialer.Dial(endpoint, nil)
			if err == nil {
				_ = second.Close()
				t.Fatal("second connection exceeded the configured limit")
			}
			if response == nil || response.StatusCode != http.StatusServiceUnavailable {
				t.Fatalf("second connection response = %v, want %d", response, http.StatusServiceUnavailable)
			}
			_ = response.Body.Close()
			_ = first.Close()
			deadline := time.Now().Add(time.Second)
			for serverConnectionCount(s) != 0 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			third := dialWebSocketTest(t, endpoint)
			_ = third.Close()
		})
	}
}
