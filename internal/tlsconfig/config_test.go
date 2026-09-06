package tlsconfig

import (
	"crypto/tls"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateClient(t *testing.T) {
	require.NoError(t, ValidateClient(nil))
	require.NoError(t, ValidateClient(new(tls.Config)))
}

func TestValidateServer(t *testing.T) {
	require.NoError(t, ValidateServer(nil))
	require.ErrorIs(t, ValidateServer(new(tls.Config)), ErrCertificateRequired)
	require.NoError(t, ValidateServer(&tls.Config{Certificates: []tls.Certificate{{}}}))
	require.NoError(t, ValidateServer(&tls.Config{GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		return nil, nil
	}}))
}

func TestCloneReturnsIndependentConfiguration(t *testing.T) {
	config := &tls.Config{MinVersion: tls.VersionTLS12}
	cloned := Clone(config)
	require.NotSame(t, config, cloned)
	config.MinVersion = tls.VersionTLS13
	require.Equal(t, uint16(tls.VersionTLS12), cloned.MinVersion)
	require.Nil(t, Clone(nil))
}
