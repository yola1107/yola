package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"yola/instance"
	"yola/internal/grpcendpoint"
	"yola/locate"

	"github.com/go-kratos/kratos/v3/registry"
	"google.golang.org/grpc/attributes"
	"google.golang.org/grpc/resolver"
)

type backendResolverBuilder struct {
	discovery registry.Discovery
	state     *backendState
}

func (b *backendResolverBuilder) Build(target resolver.Target, cc resolver.ClientConn, opts resolver.BuildOptions) (resolver.Resolver, error) {
	serviceName := strings.TrimPrefix(target.URL.Path, "/")
	if !locate.ValidServiceName(serviceName) {
		return nil, fmt.Errorf("gateway: invalid node service %q", serviceName)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &backendResolver{
		serviceName: serviceName,
		discovery:   b.discovery,
		client:      cc,
		ctx:         ctx,
		cancel:      cancel,
		secure:      secureTransport(opts),
		state:       b.state,
	}
	go r.watch()
	return r, nil
}

func (*backendResolverBuilder) Scheme() string { return "discovery" }

type backendResolver struct {
	serviceName string
	discovery   registry.Discovery
	client      resolver.ClientConn
	ctx         context.Context
	cancel      context.CancelFunc
	secure      bool
	state       *backendState
}

func (r *backendResolver) watch() {
	for r.ctx.Err() == nil {
		if !r.retry(r.watchOnce()) {
			return
		}
	}
}

func (r *backendResolver) watchOnce() error {
	watcher, err := r.discovery.Watch(r.ctx, r.serviceName)
	if err == nil && watcher == nil {
		err = errors.New("nil discovery watcher")
	}
	if err != nil {
		// Watch creation failures are reported even when discovery returns cancellation.
		r.client.ReportError(err)
		return err
	}

	err = r.consume(watcher)
	if stopErr := watcher.Stop(); stopErr != nil && !errors.Is(stopErr, context.Canceled) {
		slog.Error("stop node discovery failed", "service", r.serviceName, "error", stopErr)
	}
	if !errors.Is(err, context.Canceled) && r.ctx.Err() == nil {
		r.client.ReportError(err)
	}
	return err
}

func (r *backendResolver) consume(watcher registry.Watcher) error {
	for {
		instances, err := watcher.Next()
		if err != nil {
			return err
		}
		r.update(instances)
	}
}

func (r *backendResolver) retry(err error) bool {
	if errors.Is(err, context.Canceled) || r.ctx.Err() != nil {
		return false
	}
	slog.Error("watch node discovery failed", "service", r.serviceName, "error", err)
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-r.ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (r *backendResolver) update(instances []*registry.ServiceInstance) {
	snapshot, addresses, err := resolveBackendInstances(r.serviceName, instances, r.secure)
	if err != nil {
		r.reject(err)
		return
	}
	sticky, initialized := r.state.mode()
	if initialized && snapshot.available && snapshot.sticky != sticky {
		r.reject(fmt.Errorf(
			"gateway: node service %q changed sticky mode from %t to %t; restart Gateway to apply",
			r.serviceName,
			sticky,
			snapshot.sticky,
		))
		return
	}
	if len(addresses) == 0 {
		r.state.markUnavailable()
		_ = r.client.UpdateState(resolver.State{})
		return
	}
	// Keep the previous complete snapshot until grpc-go accepts the replacement.
	if err := r.client.UpdateState(resolver.State{Addresses: addresses}); err != nil {
		r.reject(err)
		return
	}
	r.state.store(snapshot)
}

func (r *backendResolver) reject(err error) {
	r.state.markUnavailable()
	_ = r.client.UpdateState(resolver.State{})
	r.client.ReportError(err)
}

func resolveBackendInstances(serviceName string, instances []*registry.ServiceInstance, secure bool) (backendSnapshot, []resolver.Address, error) {
	seen := make(map[string]struct{}, len(instances))
	addresses := make([]resolver.Address, 0, len(instances))
	identities := newBackendIdentities(len(instances))
	sticky := false
	found := false
	for _, service := range instances {
		if service == nil || service.Name != serviceName {
			continue
		}
		currentSticky, host, err := resolveBackendInstance(serviceName, service, secure)
		if err != nil {
			return backendSnapshot{}, nil, err
		}
		if found && sticky != currentSticky {
			return backendSnapshot{}, nil, fmt.Errorf("gateway: node service %q has conflicting sticky metadata", serviceName)
		}
		sticky, found = currentSticky, true
		if err := identities.add(serviceName, service.ID, host); err != nil {
			return backendSnapshot{}, nil, err
		}
		if _, ok := seen[host]; ok {
			continue
		}
		seen[host] = struct{}{}
		// Address ServerName outranks TLS credentials; leaving it empty preserves
		// an explicit TLS ServerName and otherwise uses the discovery authority.
		addresses = append(addresses, resolver.Address{
			Addr:       host,
			Attributes: attributes.New(rawServiceInstanceKey, service),
		})
	}
	return backendSnapshot{
		available:    len(addresses) > 0,
		sticky:       sticky,
		hostByNodeID: identities.hostByNodeID,
	}, addresses, nil
}

func resolveBackendInstance(serviceName string, service *registry.ServiceInstance, secure bool) (bool, string, error) {
	sticky, err := instance.IsSticky(service.Metadata)
	if err != nil {
		return false, "", fmt.Errorf("gateway: node service %q: %w", serviceName, err)
	}
	host, endpointMismatch := grpcInstanceHost(service.Endpoints, secure)
	if host == "" {
		if !endpointMismatch {
			return false, "", fmt.Errorf("gateway: node service %q has no gRPC endpoint", serviceName)
		}
		wantScheme := "grpc://"
		if secure {
			wantScheme = "grpcs://"
		}
		return false, "", fmt.Errorf(
			"gateway: node service %q has no %s endpoint for configured transport",
			serviceName,
			wantScheme,
		)
	}
	if sticky && service.ID == "" {
		return false, "", fmt.Errorf("gateway: sticky node service %q has an empty NodeID", serviceName)
	}
	return sticky, host, nil
}

type backendIdentities struct {
	hostByNodeID map[string]string
	nodeIDByHost map[string]string
}

func newBackendIdentities(capacity int) backendIdentities {
	return backendIdentities{
		hostByNodeID: make(map[string]string, capacity),
		nodeIDByHost: make(map[string]string, capacity),
	}
}

func (i backendIdentities) add(serviceName, nodeID, host string) error {
	if nodeID == "" {
		return nil
	}
	if previousHost, exists := i.hostByNodeID[nodeID]; exists && previousHost != host {
		return fmt.Errorf(
			"gateway: node service %q has duplicate NodeID %q with different endpoints",
			serviceName,
			nodeID,
		)
	}
	if previousNodeID, exists := i.nodeIDByHost[host]; exists && previousNodeID != nodeID {
		return fmt.Errorf(
			"gateway: node service %q endpoint %q belongs to multiple NodeIDs",
			serviceName,
			host,
		)
	}
	i.hostByNodeID[nodeID] = host
	i.nodeIDByHost[host] = nodeID
	return nil
}

func (r *backendResolver) ResolveNow(resolver.ResolveNowOptions) {}

func (r *backendResolver) Close() {
	r.cancel()
}

func secureTransport(opts resolver.BuildOptions) bool {
	creds := opts.DialCreds
	if creds == nil && opts.CredsBundle != nil {
		creds = opts.CredsBundle.TransportCredentials()
	}
	return creds != nil && !strings.EqualFold(creds.Info().SecurityProtocol, "insecure")
}

func grpcInstanceHost(endpoints []string, secure bool) (string, bool) {
	endpointMismatch := false
	for _, endpoint := range endpoints {
		host, err := grpcendpoint.Host(endpoint, secure)
		if err == nil {
			return host, false
		}
		if errors.Is(err, grpcendpoint.ErrTransportSecurity) {
			endpointMismatch = true
		}
	}
	return "", endpointMismatch
}
