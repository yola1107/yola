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
	t.Run("context", func(t *testing.T) {
		var configured options
		WithContext(ctx)(&configured)
		require.Equal(t, ctx, configured.ctx)
	})
	t.Run("URL", func(t *testing.T) {
		var configured options
		WithURL("nats://127.0.0.1:4222")(&configured)
		require.Equal(t, "nats://127.0.0.1:4222", configured.url)
	})
	t.Run("timeout", func(t *testing.T) {
		var configured options
		WithTimeout(3 * time.Second)(&configured)
		require.Equal(t, 3*time.Second, configured.timeout)
	})
	t.Run("queue capacity", func(t *testing.T) {
		var configured options
		WithQueueCapacity(32)(&configured)
		require.Equal(t, 32, configured.queueCapacity)
	})
	t.Run("maximum payload size", func(t *testing.T) {
		var configured options
		WithMaxPayloadBytes(4096)(&configured)
		require.Equal(t, 4096, configured.maxPayloadBytes)
	})
	t.Run("user info", func(t *testing.T) {
		var configured options
		WithUserInfo("test-user", "test-password")(&configured)
		require.Equal(t, []string{"test-user", "test-password"}, []string{configured.username, configured.password})
	})
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
