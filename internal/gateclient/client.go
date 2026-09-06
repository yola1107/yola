package gateclient

import (
	"context"
	"crypto/tls"
	"errors"
	"sync"
	"time"

	"yola/api/cluster/v1"
	"yola/internal/clusterroute"
	"yola/internal/grpcendpoint"
	"yola/internal/tlsconfig"
	"yola/locate"

	kgrpc "github.com/go-kratos/kratos/v3/transport/grpc"
	"golang.org/x/sync/singleflight"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

const (
	serviceConfig = `{"loadBalancingConfig":[{"pick_first":{}}]}`
	idleTimeout   = 5 * time.Minute
)

var (
	ErrUnavailable       = errors.New("gateway instance is unavailable")
	ErrInvalidEndpoint   = grpcendpoint.ErrInvalidEndpoint
	ErrTransportSecurity = grpcendpoint.ErrTransportSecurity
)

type Client struct {
	idle      time.Duration
	tlsConfig *tls.Config
	ctx       context.Context
	cancel    context.CancelFunc
	dials     singleflight.Group
	mu        sync.Mutex
	byHost    map[string]*rpc
}

type rpc struct {
	host      string
	conn      *grpc.ClientConn
	client    v1.GatewayClient
	lastUsed  time.Time
	idleTimer *time.Timer
	inflight  int
}

// New creates a connection pool; callers own every Push/Kick deadline.
func New(tlsConfig *tls.Config) *Client {
	ctx, cancel := context.WithCancel(context.Background())
	return &Client{
		idle:      idleTimeout,
		tlsConfig: tlsconfig.Clone(tlsConfig),
		ctx:       ctx,
		cancel:    cancel,
		byHost:    make(map[string]*rpc),
	}
}

func (c *Client) Push(ctx context.Context, binding locate.GateBinding, command int32, msg proto.Message) error {
	body, err := proto.Marshal(msg)
	if err != nil {
		return err
	}
	entry, err := c.acquire(ctx, binding.GateEndpoint)
	if err != nil {
		return err
	}
	defer c.release(entry)
	_, err = entry.client.Push(ctx, &v1.PushRequest{
		Route: clusterroute.FromBinding(binding), Command: command, Body: body,
	})
	return err
}

func (c *Client) Kick(ctx context.Context, binding locate.GateBinding, code int32) error {
	entry, err := c.acquire(ctx, binding.GateEndpoint)
	if err != nil {
		return err
	}
	defer c.release(entry)
	_, err = entry.client.Kick(ctx, &v1.KickRequest{
		Route: clusterroute.FromBinding(binding), Code: code,
	})
	return err
}

func (c *Client) acquire(ctx context.Context, endpoint string) (*rpc, error) {
	host, err := grpcendpoint.Host(endpoint, c.tlsConfig != nil)
	if err != nil {
		return nil, err
	}
	for {
		entry, err := c.cached(host)
		if err != nil {
			return nil, err
		}
		if entry != nil {
			return entry, nil
		}
		result := c.dials.DoChan(host, func() (any, error) {
			return nil, c.connect(host)
		})
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-c.ctx.Done():
			return nil, ErrUnavailable
		case outcome := <-result:
			if outcome.Err != nil {
				return nil, outcome.Err
			}
		}
	}
}

func (c *Client) cached(host string) (*rpc, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ctx.Err() != nil || c.byHost == nil {
		return nil, ErrUnavailable
	}
	if entry := c.byHost[host]; entry != nil {
		entry.inflight++
		if entry.idleTimer != nil {
			entry.idleTimer.Stop()
			entry.idleTimer = nil
		}
		return entry, nil
	}
	return nil, nil
}

func (c *Client) connect(host string) error {
	opts := []kgrpc.ClientOption{
		kgrpc.WithEndpoint("direct:///" + host),
		kgrpc.WithTimeout(0), // Disable Kratos' implicit 2s deadline; caller context owns it.
	}
	if c.tlsConfig != nil {
		opts = append(opts, kgrpc.WithTLSConfig(c.tlsConfig.Clone()))
	}
	opts = append(opts, kgrpc.WithOptions(grpc.WithDefaultServiceConfig(serviceConfig)))
	conn, err := kgrpc.NewClient(c.ctx, opts...)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byHost == nil {
		_ = conn.Close()
		return ErrUnavailable
	}
	if c.byHost[host] != nil {
		_ = conn.Close()
		return nil
	}
	entry := &rpc{
		host: host, conn: conn, client: v1.NewGatewayClient(conn),
		lastUsed: time.Now(),
	}
	c.byHost[host] = entry
	entry.idleTimer = time.AfterFunc(c.idle, func() { c.evict(entry) })
	return nil
}

func (c *Client) release(entry *rpc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byHost[entry.host] != entry {
		return
	}
	entry.inflight--
	entry.lastUsed = time.Now()
	if entry.inflight == 0 {
		entry.idleTimer = time.AfterFunc(c.idle, func() { c.evict(entry) })
	}
}

func (c *Client) evict(entry *rpc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byHost == nil || c.byHost[entry.host] != entry || entry.inflight > 0 || time.Since(entry.lastUsed) < c.idle {
		return
	}
	delete(c.byHost, entry.host)
	entry.idleTimer = nil
	_ = entry.conn.Close()
}

func (c *Client) Close() error {
	c.cancel()
	c.mu.Lock()
	if c.byHost == nil {
		c.mu.Unlock()
		return nil
	}
	connections := make([]*grpc.ClientConn, 0, len(c.byHost))
	for _, entry := range c.byHost {
		if entry.idleTimer != nil {
			entry.idleTimer.Stop()
		}
		connections = append(connections, entry.conn)
	}
	c.byHost = nil
	c.mu.Unlock()
	errs := make([]error, 0, len(connections))
	for _, conn := range connections {
		errs = append(errs, conn.Close())
	}
	return errors.Join(errs...)
}
