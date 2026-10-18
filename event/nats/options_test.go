package nats

import (
	"context"
	"crypto/tls"
	"testing"
	"time"

	"yola/event"

	"github.com/stretchr/testify/require"
)

func TestOptionSetters(t *testing.T) {
	ctx := context.Background()
	var configured options
	for _, option := range []Option{
		WithContext(ctx),
		WithURL("nats://127.0.0.1:4222"),
		WithTimeout(3 * time.Second),
		WithQueueCapacity(32),
		WithMaxPayloadBytes(4096),
		WithUserInfo("test-user", "test-password"),
	} {
		option(&configured)
	}
	require.Equal(t, options{
		ctx:             ctx,
		url:             "nats://127.0.0.1:4222",
		timeout:         3 * time.Second,
		queueCapacity:   32,
		maxPayloadBytes: 4096,
		username:        "test-user",
		password:        "test-password",
	}, configured)
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
	configured := &options{}
	WithTLS(tlsConfig)(configured)
	tlsConfig.MinVersion = tls.VersionTLS13
	require.Equal(t, uint16(tls.VersionTLS12), configured.tls.MinVersion)
}

func TestNewRejectsInvalidConfiguration(t *testing.T) {
	for _, test := range []struct {
		name   string
		option Option
	}{
		{name: "timeout", option: WithTimeout(0)},
		{name: "queue capacity", option: WithQueueCapacity(0)},
		{name: "payload size", option: WithMaxPayloadBytes(0)},
		{name: "TLS", option: WithTLS(nil)},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := New(test.option)
			require.Error(t, err)
		})
	}

	var nilCtx context.Context
	_, err := New(WithContext(nilCtx))
	require.ErrorIs(t, err, event.ErrInvalidContext)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = New(WithContext(canceled))
	require.ErrorIs(t, err, context.Canceled)
}
