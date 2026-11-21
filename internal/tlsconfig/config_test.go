package tlsconfig

import (
	"crypto/tls"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateClient(t *testing.T) {
	require.NoError(t, ValidateClient(nil))
	require.NoError(t, ValidateClient(&tls.Config{}))
}

func TestValidateServer(t *testing.T) {
	require.NoError(t, ValidateServer(nil))
	require.ErrorIs(t, ValidateServer(&tls.Config{}), ErrCertificateRequired)
	require.NoError(t, ValidateServer(&tls.Config{Certificates: []tls.Certificate{{}}}))
	require.NoError(t, ValidateServer(&tls.Config{GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		return nil, nil
	}}))
}
