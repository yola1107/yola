// Package tlsconfig 集中校验 Yola transport 共用的 TLS 约束。
package tlsconfig

import (
	"crypto/tls"
	"errors"
)

var (
	ErrVerificationDisabled = errors.New("TLS certificate verification must be enabled")
	ErrCertificateRequired  = errors.New("TLS certificate is required")
)

// Clone 返回独立的 TLS 配置。
func Clone(config *tls.Config) *tls.Config {
	if config == nil {
		return nil
	}
	return config.Clone()
}

// ValidateClient 拒绝关闭证书验证的 TLS 配置。
func ValidateClient(config *tls.Config) error {
	if config != nil && config.InsecureSkipVerify {
		return ErrVerificationDisabled
	}
	return nil
}

// ValidateServer 要求启用的 TLS 配置保留证书验证并提供证书来源。
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
