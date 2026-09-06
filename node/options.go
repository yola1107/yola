package node

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/url"
	"time"

	"yola/internal/grpcendpoint"
	"yola/internal/tlsconfig"
	"yola/locate"

	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/transport/grpc"
)

// Option configures a Node server.
type Option func(*options) error

type options struct {
	grpcOptions   []grpc.ServerOption
	clientTLS     *tls.Config
	pushTimeout   time.Duration
	network       string
	address       string
	listener      net.Listener
	advertiseHost string
	endpoint      *url.URL
	serverTLS     bool
	middlewares   []middleware.Middleware
	locator       locate.Locator
	drain         DrainFunc
}

func resolveOptions(opts ...Option) (options, error) {
	o := options{pushTimeout: 3 * time.Second, network: "tcp"}
	for _, opt := range opts {
		if err := opt(&o); err != nil {
			return options{}, err
		}
	}
	if err := o.resolveEndpoint(); err != nil {
		return options{}, err
	}
	if o.address == "" {
		o.address = ":0"
	}
	return o, nil
}

func (o *options) resolveEndpoint() error {
	endpoint, err := grpcendpoint.Resolve(o.endpoint, o.address, o.advertiseHost, o.serverTLS)
	if err != nil {
		return fmt.Errorf("node: %w", err)
	}
	if endpoint == nil {
		return nil
	}
	o.endpoint = endpoint
	o.grpcOptions = append(o.grpcOptions, grpc.Endpoint(endpoint))
	return nil
}

// Network configures the Node listen network.
func Network(network string) Option {
	return func(o *options) error {
		if network == "" {
			return errors.New("node: network is required")
		}
		o.network = network
		return nil
	}
}

// Address configures the Node listen address.
func Address(address string) Option {
	return func(o *options) error {
		if address == "" {
			return errors.New("node: address is required")
		}
		o.address = address
		return nil
	}
}

// AdvertiseHost publishes a reachable host while keeping the listen port from Address.
func AdvertiseHost(host string) Option {
	return func(o *options) error {
		if host == "" {
			return errors.New("node: advertise host is required")
		}
		o.advertiseHost = host
		return nil
	}
}

// Endpoint configures the Node endpoint published through service discovery.
func Endpoint(endpoint *url.URL) Option {
	return func(o *options) error {
		if endpoint == nil {
			return errors.New("node: endpoint is required")
		}
		configured := *endpoint
		o.endpoint = &configured
		return nil
	}
}

// HandlerTimeout configures the internal request handler timeout.
func HandlerTimeout(timeout time.Duration) Option {
	return func(o *options) error {
		if timeout < 0 {
			return errors.New("node: handler timeout cannot be negative")
		}
		o.grpcOptions = append(o.grpcOptions, grpc.Timeout(timeout))
		return nil
	}
}

// Listener configures an existing Node listener.
func Listener(listener net.Listener) Option {
	return func(o *options) error {
		if listener == nil {
			return errors.New("node: listener is required")
		}
		o.listener = listener
		return nil
	}
}

// ServerTLS configures TLS for the internal Node server.
func ServerTLS(config *tls.Config) Option {
	return func(o *options) error {
		if config == nil {
			return errors.New("node: TLS config is required")
		}
		if err := tlsconfig.ValidateServer(config); err != nil {
			return fmt.Errorf("node: %w", err)
		}
		o.grpcOptions = append(o.grpcOptions, grpc.TLSConfig(tlsconfig.Clone(config)))
		o.serverTLS = true
		return nil
	}
}

// Middleware configures middleware for protobuf command handlers.
func Middleware(m ...middleware.Middleware) Option {
	return func(o *options) error {
		o.middlewares = append(o.middlewares, m...)
		return nil
	}
}

// Locator configures Node and Gate location storage together.
// Node and Gate lookups are always configured as a pair: sticky routing needs the
// Node binding and PushToUID needs the Gate binding, so a half-configured Locator
// would only fail at request time.
func Locator(locator locate.Locator) Option {
	return func(o *options) error {
		if locator == nil {
			return errors.New("node: locator is required")
		}
		o.locator = locator
		return nil
	}
}

// ClientTLS configures verified TLS for Node-to-Gateway gRPC calls.
func ClientTLS(config *tls.Config) Option {
	return func(o *options) error {
		if config == nil {
			return errors.New("node: gRPC TLS config is required")
		}
		if err := tlsconfig.ValidateClient(config); err != nil {
			return fmt.Errorf("node: gRPC %w", err)
		}
		o.clientTLS = tlsconfig.Clone(config)
		return nil
	}
}

// PushTimeout configures the timeout for Node-to-Gateway push requests.
func PushTimeout(timeout time.Duration) Option {
	return func(o *options) error {
		if timeout <= 0 {
			return errors.New("node: push timeout must be positive")
		}
		o.pushTimeout = timeout
		return nil
	}
}

// Drain registers the business shutdown barrier invoked after accepted requests finish.
// A returned error prevents Stop from actively releasing the Node epoch.
func Drain(fn DrainFunc) Option {
	return func(o *options) error {
		o.drain = fn
		return nil
	}
}
