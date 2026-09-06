// Package grpcendpoint validates advertised gRPC endpoints shared by Yola servers.
package grpcendpoint

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

var (
	ErrInvalidEndpoint   = errors.New("invalid gRPC endpoint")
	ErrTransportSecurity = errors.New("gRPC endpoint transport security mismatch")
)

func Advertise(listenAddress, advertiseHost string, secure bool) (*url.URL, error) {
	listenHost, port, err := net.SplitHostPort(listenAddress)
	if err != nil {
		return nil, fmt.Errorf("invalid listen address %q: %w", listenAddress, err)
	}
	portNumber, err := strconv.ParseUint(port, 10, 16)
	if err != nil || portNumber == 0 {
		return nil, fmt.Errorf("invalid listen port %q", port)
	}
	if isLoopback(listenHost) && !isLoopback(advertiseHost) {
		return nil, fmt.Errorf(
			"loopback listen host %q cannot advertise non-loopback host %q",
			listenHost,
			advertiseHost,
		)
	}
	endpoint := &url.URL{
		Scheme: scheme(secure),
		Host:   net.JoinHostPort(advertiseHost, port),
	}
	if err := Validate(endpoint, secure); err != nil {
		return nil, err
	}
	return endpoint, nil
}

// Resolve returns an explicit endpoint or derives one from the advertised host.
func Resolve(endpoint *url.URL, listenAddress, advertiseHost string, secure bool) (*url.URL, error) {
	if endpoint != nil {
		if err := Validate(endpoint, secure); err != nil {
			return nil, err
		}
		return endpoint, nil
	}
	if advertiseHost == "" {
		return nil, nil
	}
	if listenAddress == "" {
		return nil, errors.New("address is required when advertise host is set")
	}
	return Advertise(listenAddress, advertiseHost, secure)
}

func Validate(endpoint *url.URL, secure bool) error {
	if endpoint == nil {
		return invalidError("gRPC endpoint is required")
	}
	if endpoint.Scheme != "grpc" && endpoint.Scheme != "grpcs" {
		return invalidError("invalid gRPC endpoint scheme %q", endpoint.Scheme)
	}
	wantScheme := scheme(secure)
	if endpoint.Scheme != wantScheme {
		return transportError(wantScheme)
	}
	return validate(endpoint)
}

// Host parses an advertised endpoint and returns its validated host:port.
func Host(raw string, secure bool) (string, error) {
	endpoint, err := url.Parse(raw)
	if err != nil {
		return "", invalidError("parse gRPC endpoint %q: %v", raw, err)
	}
	if err := Validate(endpoint, secure); err != nil {
		return "", err
	}
	return endpoint.Host, nil
}

func validate(endpoint *url.URL) error {
	if endpoint.Host == "" {
		return invalidError("gRPC endpoint host is required")
	}
	if endpoint.User != nil || endpoint.Path != "" || endpoint.RawPath != "" ||
		endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" || endpoint.Opaque != "" {
		return invalidError("gRPC endpoint must not include userinfo, path, query, or fragment")
	}
	host, port, err := net.SplitHostPort(endpoint.Host)
	if err != nil || host == "" {
		return invalidError("invalid gRPC endpoint host %q", endpoint.Host)
	}
	if !validHost(host) {
		return invalidError("invalid gRPC endpoint host %q", endpoint.Host)
	}
	portNumber, err := strconv.ParseUint(port, 10, 16)
	if err != nil || portNumber == 0 {
		return invalidError("invalid gRPC endpoint port %q", port)
	}
	return nil
}

func validHost(host string) bool {
	if _, err := netip.ParseAddr(host); err == nil {
		return true
	}
	if strings.ContainsAny(host, "%@/?#[]:\\") {
		return false
	}
	return !strings.ContainsFunc(host, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	})
}

func transportError(wantScheme string) error {
	return endpointError{
		message: fmt.Sprintf("gRPC endpoint must use %s:// with a host", wantScheme),
		cause:   ErrTransportSecurity,
	}
}

func invalidError(format string, args ...any) error {
	return endpointError{message: fmt.Sprintf(format, args...), cause: ErrInvalidEndpoint}
}

type endpointError struct {
	message string
	cause   error
}

func (e endpointError) Error() string { return e.message }

func (e endpointError) Unwrap() error { return e.cause }

func scheme(secure bool) string {
	if secure {
		return "grpcs"
	}
	return "grpc"
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}
