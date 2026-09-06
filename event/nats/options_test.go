package nats

import (
	"context"
	"crypto/tls"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOptionSetters(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		name   string
		option Option
		value  func(options) any
		want   any
	}{
		{
			name:   "context",
			option: WithContext(ctx),
			value:  func(o options) any { return o.ctx },
			want:   ctx,
		},
		{
			name:   "URL",
			option: WithURL("nats://127.0.0.1:4222"),
			value:  func(o options) any { return o.url },
			want:   "nats://127.0.0.1:4222",
		},
		{
			name:   "timeout",
			option: WithTimeout(3 * time.Second),
			value:  func(o options) any { return o.timeout },
			want:   3 * time.Second,
		},
		{
			name:   "queue capacity",
			option: WithQueueCapacity(32),
			value:  func(o options) any { return o.queueCapacity },
			want:   32,
		},
		{
			name:   "maximum payload size",
			option: WithMaxPayloadBytes(4096),
			value:  func(o options) any { return o.maxPayloadBytes },
			want:   4096,
		},
		{
			name:   "user info",
			option: WithUserInfo("test-user", "test-password"),
			value:  func(o options) any { return []string{o.username, o.password} },
			want:   []string{"test-user", "test-password"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			configured := options{}
			test.option(&configured)

			require.Equal(t, test.want, test.value(configured))
		})
	}
}

func TestRedactedURL(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  string
		want string
	}{
		{name: "credentials", raw: "nats://test-user:test-password@127.0.0.1:4222/events?cluster=test", want: "nats://127.0.0.1:4222/events?cluster=test"},
		{name: "missing scheme", raw: "127.0.0.1", want: "nats://"},
		{name: "malformed URL", raw: "://127.0.0.1:4222", want: "nats://"},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, RedactedURL(test.raw))
		})
	}
}

func TestWithTLSClonesConfiguration(t *testing.T) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	configured := new(options)
	WithTLS(tlsConfig)(configured)
	tlsConfig.MinVersion = tls.VersionTLS13
	require.Equal(t, uint16(tls.VersionTLS12), configured.tls.MinVersion)
}
