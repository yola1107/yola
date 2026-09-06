package websocket

import (
	"context"
	"crypto/tls"
	"errors"
	"testing"
	"time"
)

func TestClientRejectsInvalidConfiguration(t *testing.T) {
	ctx := context.Background()
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	tests := []struct {
		name    string
		ctx     context.Context
		opts    []ClientOption
		want    string
		wantErr error
	}{
		{
			name:    "canceled context",
			ctx:     canceled,
			opts:    []ClientOption{WithServiceName("game"), WithToken("token")},
			wantErr: context.Canceled,
		},
		{
			name: "nil context",
			opts: []ClientOption{WithServiceName("game"), WithToken("token")},
			want: "websocket: context is nil",
		},
		{
			name: "codec",
			ctx:  ctx,
			opts: []ClientOption{WithServiceName("game"), WithToken("token"), WithCodec(nil)},
			want: "websocket: codec is required",
		},
		{
			name: "channel config",
			ctx:  ctx,
			opts: []ClientOption{WithServiceName("game"), WithToken("token"), WithChannelConfig(nil)},
			want: "websocket: channel config is required",
		},
		{
			name: "authentication timeout",
			ctx:  ctx,
			opts: []ClientOption{WithServiceName("game"), WithToken("token"), WithTimeout(0)},
			want: "websocket: authentication timeout must be positive",
		},
		{
			name: "ping interval",
			ctx:  ctx,
			opts: []ClientOption{WithServiceName("game"), WithToken("token"), WithPingInterval(0)},
			want: "websocket: ping interval must be positive",
		},
		{
			name: "ping interval and read deadline",
			ctx:  ctx,
			opts: []ClientOption{
				WithServiceName("game"),
				WithToken("token"),
				WithPingInterval(time.Second),
				WithChannelConfig(&ChannelConfig{
					WriteTimeout:  time.Second,
					ReadDeadline:  time.Second,
					SendQueueSize: 1,
				}),
			},
			want: "websocket: ping interval must be shorter than channel read deadline",
		},
		{
			name: "request timeout",
			ctx:  ctx,
			opts: []ClientOption{WithServiceName("game"), WithToken("token"), WithRequestTimeout(0)},
			want: "websocket: request timeout must be positive",
		},
		{
			name: "missing service",
			ctx:  ctx,
			opts: []ClientOption{WithToken("token")},
			want: "websocket: service name is required",
		},
		{
			name: "missing token",
			ctx:  ctx,
			opts: []ClientOption{WithServiceName("game")},
			want: "websocket: token is required",
		},
		{
			name: "callback queue",
			ctx:  ctx,
			opts: []ClientOption{WithServiceName("game"), WithToken("token"), WithCallbackQueueSize(0)},
			want: "websocket: callback queue size must be positive",
		},
		{
			name: "TLS verification",
			ctx:  ctx,
			opts: []ClientOption{
				WithServiceName("game"),
				WithToken("token"),
				WithTLSConfig(&tls.Config{InsecureSkipVerify: true}),
			},
			want: "websocket: TLS certificate verification must be enabled",
		},
		{
			name: "invalid endpoint",
			ctx:  ctx,
			opts: []ClientOption{
				WithEndpoint("http://127.0.0.1:1"),
				WithServiceName("game"),
				WithToken("token"),
			},
			want: "client: invalid URL: WebSocket endpoint must use ws or wss with a host",
		},
		{
			name: "TLS endpoint",
			ctx:  ctx,
			opts: []ClientOption{
				WithEndpoint("ws://127.0.0.1:1"),
				WithServiceName("game"),
				WithToken("token"),
				WithTLSConfig(new(tls.Config)),
			},
			want: "websocket: TLS config requires a wss endpoint",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewClient(test.ctx, test.opts...)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("NewClient() error = %v, want %v", err, test.wantErr)
				}
			} else if err == nil || err.Error() != test.want {
				t.Fatalf("NewClient() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestResolveClientOptionsClonesMutableConfiguration(t *testing.T) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	channelConfig := &ChannelConfig{WriteTimeout: time.Second, ReadDeadline: 20 * time.Second, SendQueueSize: 3}
	handlers := map[int32]PushHandler{1: func([]byte) {}}
	o, err := resolveClientOptions(
		WithEndpoint("example.test:443"),
		WithServiceName("game"),
		WithToken("synthetic-token"),
		WithTLSConfig(tlsConfig),
		WithChannelConfig(channelConfig),
		WithPushHandler(handlers),
	)
	if err != nil {
		t.Fatal(err)
	}
	if o.tlsConf == tlsConfig || o.channel == channelConfig {
		t.Fatal("resolved options retained caller configuration")
	}
	if o.endpoint != "wss://example.test:443" {
		t.Fatalf("resolved endpoint = %q", o.endpoint)
	}
	tlsConfig.MinVersion = tls.VersionTLS13
	channelConfig.SendQueueSize = 4
	delete(handlers, 1)
	handlers[2] = func([]byte) {}
	if o.tlsConf.MinVersion != tls.VersionTLS12 || o.channel.SendQueueSize != 3 {
		t.Fatal("resolved options changed with caller configuration")
	}
	if o.pushHandler[1] == nil {
		t.Fatal("resolved push handlers changed with caller map")
	}
	if _, ok := o.pushHandler[2]; ok {
		t.Fatal("resolved push handlers retained caller map")
	}
}

func TestParseURLRejectsUnsupportedEndpoints(t *testing.T) {
	for _, endpoint := range []string{"http://127.0.0.1:3102", "ws:///socket", "://bad"} {
		if _, err := parseURL(endpoint, true); err == nil {
			t.Fatalf("parseURL(%q) succeeded", endpoint)
		}
	}
	u, err := parseURL("127.0.0.1:3102", true)
	if err != nil || u.String() != "ws://127.0.0.1:3102" {
		t.Fatalf("parseURL() = %v, %v", u, err)
	}
}
