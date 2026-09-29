package gateway

import (
	"context"

	"github.com/go-kratos/kratos/v3/registry"
	"github.com/go-kratos/kratos/v3/selector"
	"github.com/go-kratos/kratos/v3/selector/wrr"
	"google.golang.org/grpc/balancer"
	"google.golang.org/grpc/balancer/base"
)

const (
	_backendBalancerName   = "yola_wrr"
	_rawServiceInstanceKey = "rawServiceInstance"
)

type nodeIDContextKey struct{}

// Kratos v3.0.0 在初始化前捕获全局 selector；Yola 还需要精确 NodeID 路由，
// 因此将适配器保留在 Gateway 内。
func init() {
	balancer.Register(base.NewBalancerBuilder(
		_backendBalancerName,
		&backendPickerBuilder{},
		base.Config{HealthCheck: true},
	))
}

func withNodeID(ctx context.Context, nodeID string) context.Context {
	if nodeID == "" {
		return ctx
	}
	return context.WithValue(ctx, nodeIDContextKey{}, nodeID)
}

type backendPickerBuilder struct{}

func (*backendPickerBuilder) Build(info base.PickerBuildInfo) balancer.Picker {
	if len(info.ReadySCs) == 0 {
		return base.NewErrPicker(balancer.ErrNoSubConnAvailable)
	}
	nodes := make([]selector.Node, 0, len(info.ReadySCs))
	exact := make(map[string]balancer.SubConn, len(info.ReadySCs))
	for conn, ready := range info.ReadySCs {
		var service *registry.ServiceInstance
		if ready.Address.Attributes != nil {
			service, _ = ready.Address.Attributes.Value(_rawServiceInstanceKey).(*registry.ServiceInstance)
		}
		nodes = append(nodes, &backendSubConn{
			Node:    selector.NewNode("grpc", ready.Address.Addr, service),
			subConn: conn,
		})
		if service != nil && service.ID != "" {
			exact[service.ID] = conn
		}
	}
	wrrSelector := wrr.New()
	wrrSelector.Apply(nodes)
	return &backendPicker{selector: wrrSelector, exact: exact}
}

type backendPicker struct {
	selector selector.Selector
	exact    map[string]balancer.SubConn
}

func (p *backendPicker) Pick(info balancer.PickInfo) (balancer.PickResult, error) {
	if nodeID, _ := info.Ctx.Value(nodeIDContextKey{}).(string); nodeID != "" {
		conn := p.exact[nodeID]
		if conn == nil {
			return balancer.PickResult{}, balancer.ErrNoSubConnAvailable
		}
		return balancer.PickResult{SubConn: conn}, nil
	}
	node, done, err := p.selector.Select(info.Ctx)
	if err != nil {
		return balancer.PickResult{}, err
	}
	return balancer.PickResult{
		SubConn: node.(*backendSubConn).subConn,
		Done: func(di balancer.DoneInfo) {
			done(info.Ctx, selector.DoneInfo{Err: di.Err})
		},
	}, nil
}

type backendSubConn struct {
	selector.Node
	subConn balancer.SubConn
}
