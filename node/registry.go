package node

import (
	"context"
	"errors"

	"github.com/go-kratos/kratos/v3/registry"
)

// Registrar 适配 Kratos v3.0.0 的启动时序，在 Node 就绪后才调用注册器。
// 注册器仍须自行保证跨进程的记录所有权，例如 yola/registry/etcd。
func (s *Server) Registrar(registrar registry.Registrar) registry.Registrar {
	return &readyRegistrar{Registrar: registrar, server: s}
}

type readyRegistrar struct {
	registry.Registrar
	server *Server
}

func (r *readyRegistrar) Register(ctx context.Context, service *registry.ServiceInstance) error {
	s := r.server
	select {
	case <-s.ready:
	case <-ctx.Done():
		// Start 失败会取消 Kratos 注册 context，保留已发布的原始启动错误。
		select {
		case <-s.ready:
			if s.readyErr != nil {
				return s.readyErr
			}
		default:
		}
		return ctx.Err()
	}
	if s.readyErr != nil {
		return s.readyErr
	}
	if !s.requests.admit() {
		return errors.New("node: server is stopping or stopped")
	}
	defer s.requests.done()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.lease.Load().valid(); err != nil {
		return err
	}
	identity := s.currentIdentity()
	if service == nil || service.Name != identity.serviceName || service.ID != identity.nodeID {
		return errors.New("node: registration identity does not match")
	}
	return r.Registrar.Register(ctx, service)
}

func (s *Server) publishReady(err error) {
	s.readyOnce.Do(func() {
		s.readyErr = err
		close(s.ready)
	})
}
