package tcp

import (
	"context"
	"crypto/tls"
	"testing"
	"time"
)

func TestClientRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name string
		opts []ClientOption
		want string
	}{
		{
			name: "missing endpoint",
			opts: []ClientOption{WithAddress("")},
			want: "tcp: endpoint is required",
		},
		{
			name: "missing service name",
			opts: []ClientOption{WithServiceName("")},
			want: "tcp: service name is required",
		},
		{
			name: "missing token",
			opts: []ClientOption{WithToken("")},
			want: "tcp: token is required",
		},
		{
			name: "nil codec",
			opts: []ClientOption{WithCodec(nil)},
			want: "tcp: codec is required",
		},
		{
			name: "invalid ping interval",
			opts: []ClientOption{WithPingInterval(0)},
			want: "tcp: ping interval must be positive",
		},
		{
			name: "invalid read timeout",
			opts: []ClientOption{WithReadTimeout(0)},
			want: "tcp: read timeout must be positive",
		},
		{
			name: "ping not below read timeout",
			opts: []ClientOption{
				WithPingInterval(time.Second),
				WithReadTimeout(time.Second),
			},
			want: "tcp: read timeout must exceed ping interval",
		},
		{
			name: "invalid write timeout",
			opts: []ClientOption{WithWriteTimeout(0)},
			want: "tcp: write timeout must be positive",
		},
		{
			name: "invalid request timeout",
			opts: []ClientOption{WithRequestTimeout(0)},
			want: "tcp: request timeout must be positive",
		},
		{
			name: "invalid callback queue",
			opts: []ClientOption{WithCallbackQueueSize(0)},
			want: "tcp: callback queue size must be positive",
		},
		{
			name: "insecure TLS",
			opts: []ClientOption{WithTLSConfig(&tls.Config{InsecureSkipVerify: true})},
			want: "tcp: TLS certificate verification must be enabled",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			opts := append([]ClientOption{WithServiceName("game"), WithToken("synthetic-token")}, test.opts...)
			if _, err := NewClient(context.Background(), opts...); err == nil || err.Error() != test.want {
				t.Fatalf("NewClient() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestResolveClientOptionsClonesMutableConfiguration(t *testing.T) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	handlers := map[int32]PushHandler{1: func([]byte) {}}
	o, err := resolveClientOptions(
		WithServiceName("game"),
		WithToken("synthetic-token"),
		WithTLSConfig(tlsConfig),
		WithPushHandler(handlers),
	)
	if err != nil {
		t.Fatal(err)
	}
	if o.tlsConf == tlsConfig {
		t.Fatal("resolved options retained caller TLS config")
	}
	tlsConfig.MinVersion = tls.VersionTLS13
	delete(handlers, 1)
	handlers[2] = func([]byte) {}
	if o.tlsConf.MinVersion != tls.VersionTLS12 {
		t.Fatal("resolved TLS config changed with caller config")
	}
	if o.pushHandlers[1] == nil {
		t.Fatal("resolved push handlers changed with caller map")
	}
	if _, ok := o.pushHandlers[2]; ok {
		t.Fatal("resolved push handlers retained caller map")
	}
}
