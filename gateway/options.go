package gateway

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/url"
	"runtime"
	"time"

	"yola/internal/grpcendpoint"
	"yola/internal/tlsconfig"
	"yola/locate"
	"yola/network"

	"github.com/go-kratos/kratos/v3/registry"
	"github.com/go-kratos/kratos/v3/transport"
	"github.com/go-kratos/kratos/v3/transport/grpc"
)

var ErrInvalidCredentials = errors.New("invalid credentials")

// Authenticator authorizes client credentials for a service and returns a stable UID.
type Authenticator interface {
	Authenticate(ctx context.Context, serviceName string, token []byte, remoteIP string) (uid string, err error)
}

// ClientTransport is an external client-facing transport owned by Gateway.
// BeforeStart must validate configuration and bind resources that can prevent serving.
// Stop must be safe after every BeforeStart attempt and release partial resources.
type ClientTransport interface {
	transport.Server
	BeforeStart(context.Context) error
	SetHandler(network.ConnectionHandler) error
}

// Option configures a Gateway server.
type Option func(*options) error

type options struct {
	auth                   Authenticator
	locator                locate.Locator
	discovery              registry.Discovery
	clientTLS              *tls.Config
	rpcTimeout             time.Duration
	authTimeout            time.Duration
	leaseTTL               time.Duration
	network                string
	address                string
	listener               net.Listener
	advertiseHost          string
	endpoint               *url.URL
	serverTLS              bool
	broadcastWorkers       int
	broadcastQueueCapacity int
	transports             []ClientTransport
	grpcOptions            []grpc.ServerOption
}

func resolveOptions(opts ...Option) (options, error) {
	o := options{
		rpcTimeout:             3 * time.Second,
		authTimeout:            15 * time.Second,
		leaseTTL:               60 * time.Second,
		network:                "tcp",
		broadcastWorkers:       min(8, max(1, runtime.GOMAXPROCS(0))),
		broadcastQueueCapacity: 256,
	}
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
	switch {
	case o.auth == nil:
		return options{}, errors.New("gateway: authenticator is required")
	case o.locator == nil:
		return options{}, errors.New("gateway: locator is required")
	case o.discovery == nil:
		return options{}, errors.New("gateway: discovery is required")
	}
	return o, nil
}

func (o *options) resolveEndpoint() error {
	endpoint, err := grpcendpoint.Resolve(o.endpoint, o.address, o.advertiseHost, o.serverTLS)
	if err != nil {
		return fmt.Errorf("gateway: %w", err)
	}
	if endpoint == nil {
		return nil
	}
	o.endpoint = endpoint
	o.grpcOptions = append(o.grpcOptions, grpc.Endpoint(endpoint))
	return nil
}

// Network configures the internal gRPC listen network.
func Network(network string) Option {
	return func(o *options) error {
		if network == "" {
			return errors.New("gateway: gRPC network is required")
		}
		o.network = network
		return nil
	}
}

// Address configures the internal gRPC listen address.
func Address(address string) Option {
	return func(o *options) error {
		if address == "" {
			return errors.New("gateway: gRPC address is required")
		}
		o.address = address
		return nil
	}
}

// AdvertiseHost publishes a reachable host while keeping the listen port from Address.
func AdvertiseHost(host string) Option {
	return func(o *options) error {
		if host == "" {
			return errors.New("gateway: advertise host is required")
		}
		o.advertiseHost = host
		return nil
	}
}

// Endpoint configures the internal gRPC endpoint published through service discovery.
func Endpoint(endpoint *url.URL) Option {
	return func(o *options) error {
		if endpoint == nil {
			return errors.New("gateway: gRPC endpoint is required")
		}
		configured := *endpoint
		o.endpoint = &configured
		return nil
	}
}

// Listener configures an existing internal gRPC listener.
func Listener(listener net.Listener) Option {
	return func(o *options) error {
		if listener == nil {
			return errors.New("gateway: gRPC listener is required")
		}
		o.listener = listener
		return nil
	}
}

// ServerTLS configures TLS for the internal Gateway gRPC server.
func ServerTLS(config *tls.Config) Option {
	return func(o *options) error {
		if config == nil {
			return errors.New("gateway: TLS config is required")
		}
		if err := tlsconfig.ValidateServer(config); err != nil {
			return fmt.Errorf("gateway: %w", err)
		}
		o.grpcOptions = append(o.grpcOptions, grpc.TLSConfig(tlsconfig.Clone(config)))
		o.serverTLS = true
		return nil
	}
}

// Locator configures Gate and Node location storage.
func Locator(locator locate.Locator) Option {
	return func(o *options) error {
		if locator == nil {
			return errors.New("gateway: locator is required")
		}
		o.locator = locator
		return nil
	}
}

// Auth configures client authentication.
func Auth(auth Authenticator) Option {
	return func(o *options) error {
		o.auth = auth
		return nil
	}
}

// Discovery configures Node service discovery.
func Discovery(discovery registry.Discovery) Option {
	return func(o *options) error {
		o.discovery = discovery
		return nil
	}
}

// Transport registers external client transports owned by Gateway.
func Transport(servers ...ClientTransport) Option {
	return func(o *options) error {
		for _, server := range servers {
			if server == nil {
				return errors.New("gateway: client transport is required")
			}
		}
		o.transports = append(o.transports, servers...)
		return nil
	}
}

// BroadcastWorkers configures the number of workers used for one local fanout.
func BroadcastWorkers(workers int) Option {
	return func(o *options) error {
		if workers <= 0 {
			return errors.New("gateway: broadcast workers must be positive")
		}
		o.broadcastWorkers = workers
		return nil
	}
}

// BroadcastQueueCapacity configures the number of broadcasts waiting for local fanout.
func BroadcastQueueCapacity(capacity int) Option {
	return func(o *options) error {
		if capacity <= 0 {
			return errors.New("gateway: broadcast queue capacity must be positive")
		}
		o.broadcastQueueCapacity = capacity
		return nil
	}
}

// ClientTLS configures verified TLS for outbound Gateway gRPC calls.
func ClientTLS(config *tls.Config) Option {
	return func(o *options) error {
		if config == nil {
			return errors.New("gateway: gRPC TLS config is required")
		}
		if err := tlsconfig.ValidateClient(config); err != nil {
			return fmt.Errorf("gateway: gRPC %w", err)
		}
		o.clientTLS = tlsconfig.Clone(config)
		return nil
	}
}

// RPCTimeout sets the internal client RPC timeout.
func RPCTimeout(timeout time.Duration) Option {
	return func(o *options) error {
		if timeout <= 0 {
			return errors.New("gateway: RPC timeout must be positive")
		}
		o.rpcTimeout = timeout
		return nil
	}
}

// AuthTimeout sets the maximum lifetime of an unauthenticated connection.
func AuthTimeout(timeout time.Duration) Option {
	return func(o *options) error {
		if timeout <= 0 {
			return errors.New("gateway: authentication timeout must be positive")
		}
		o.authTimeout = timeout
		return nil
	}
}

// LeaseTTL sets the Gate location lease duration.
func LeaseTTL(ttl time.Duration) Option {
	return func(o *options) error {
		if ttl < time.Millisecond {
			return errors.New("gateway: lease TTL must be at least one millisecond")
		}
		o.leaseTTL = ttl
		return nil
	}
}
