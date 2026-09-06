// Package tlsconfig centralizes the TLS invariants shared by Yola transports.
package tlsconfig

import (
	"crypto/tls"
	"errors"
)

var (
	ErrVerificationDisabled = errors.New("TLS certificate verification must be enabled")
	ErrCertificateRequired  = errors.New("TLS certificate is required")
)

// Clone returns an independent TLS configuration.
func Clone(config *tls.Config) *tls.Config {
	if config == nil {
		return nil
	}
	return config.Clone()
}

// ValidateClient rejects TLS configurations that disable certificate verification.
func ValidateClient(config *tls.Config) error {
	if config != nil && config.InsecureSkipVerify {
		return ErrVerificationDisabled
	}
	return nil
}

// ValidateServer requires verified TLS and a certificate source when TLS is enabled.
func ValidateServer(config *tls.Config) error {
	if err := ValidateClient(config); err != nil {
		return err
	}
	if config != nil && len(config.Certificates) == 0 &&
		config.GetCertificate == nil && config.GetConfigForClient == nil {
		return ErrCertificateRequired
	}
	return nil
}
