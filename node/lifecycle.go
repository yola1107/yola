package node

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"yola/instance"
	"yola/internal/contextwait"
	"yola/locate"

	"github.com/go-kratos/kratos/v3"
	"google.golang.org/grpc"
)

// Start 在首次续租核验后开放 BeforeStart 准备的服务。
func (s *Server) Start(ctx context.Context) error {
	s.lifecycleMu.Lock()
	if s.requests.isClosed() {
		s.lifecycleMu.Unlock()
		return errors.New("node: server is stopping or stopped")
	}
	switch s.state {
	case stStarted:
		s.lifecycleMu.Unlock()
		return errors.New("node: server is already started")
	case stPrepared:
	default:
		s.lifecycleMu.Unlock()
		return errors.New("node: server is not prepared")
	}
	ready := s.lease.Load().start(s.failLifecycle)
	s.state = stStarted
	s.lifecycleMu.Unlock()
	var startErr error
	if ready != nil {
		select {
		case startErr = <-ready:
		case <-ctx.Done():
			startErr = ctx.Err()
		}
	}
	if startErr == nil {
		startErr = s.lease.Load().valid()
	}
	if startErr != nil {
		if errors.Is(startErr, context.Canceled) && s.requests.isClosed() && context.Cause(s.fatalCtx) == nil {
			return nil
		}
		return errors.Join(startErr, s.rollbackStart(ctx))
	}

	result := make(chan error, 1)
	go func() { result <- s.grpcServer.Start(ctx) }()
	select {
	case <-s.fatalCtx.Done():
		return context.Cause(s.fatalCtx)
	case err := <-result:
		if fatalErr := context.Cause(s.fatalCtx); fatalErr != nil {
			return fatalErr
		}
		if errors.Is(err, grpc.ErrServerStopped) {
			return nil
		}
		if err != nil {
			return errors.Join(err, s.rollbackStart(ctx))
		}
		return nil
	}
}

// Stop 排空请求、业务和投递，仅在全部完成后主动释放 epoch。
func (s *Server) Stop(ctx context.Context) error {
	ctx = normalizeContext(ctx)
	var firstStop bool
	var epochErr error
	s.stopOnce.Do(func() {
		firstStop = true
		s.stopErr, epochErr = s.shutdown(ctx)
	})
	if !firstStop {
		epochErr = s.lease.Load().retryRelease(ctx)
	}
	return errors.Join(s.stopErr, epochErr)
}

func (s *Server) shutdown(ctx context.Context) (error, error) {
	if preparationDone := s.beginStopping(); preparationDone != nil {
		if err := contextwait.Done(ctx, preparationDone); err != nil {
			return err, nil
		}
	}

	requestErr := s.requests.stopAndWait(ctx)
	var drainErr error
	if requestErr == nil && s.drain != nil {
		drainErr = s.drain(ctx)
	}
	// 业务 Drain 负责停止推送生产者，投递能力在此之前继续服务已接纳业务。
	deliveryErr := s.deliveries.stopAndWait(ctx)
	canRelease := requestErr == nil && drainErr == nil && deliveryErr == nil
	var renewalErr error
	if !canRelease {
		renewalErr = s.lease.Load().stopRenewal(ctx)
	}
	grpcErr := errors.Join(s.grpcServer.Stop(ctx), s.closeGRPCListener())
	var epochErr error
	if canRelease {
		epochErr = s.releaseEpoch(ctx)
	}
	slog.InfoContext(ctx, "node stopping", "pid", os.Getpid())
	return errors.Join(requestErr, drainErr, deliveryErr, renewalErr, grpcErr, s.gateways.Close()), epochErr
}

// BeforeStart 校验应用身份与 Locator，并按需申请 Node epoch。
func (s *Server) BeforeStart(ctx context.Context) (err error) {
	s.lifecycleMu.Lock()
	if s.requests.isClosed() {
		s.lifecycleMu.Unlock()
		return errors.New("node: server is stopping or stopped")
	}
	switch s.state {
	case stPreparing:
		s.lifecycleMu.Unlock()
		return errors.New("node: server is already preparing or prepared")
	case stPrepared, stStarted:
		s.requests.close()
		s.lifecycleMu.Unlock()
		return errors.New("node: server is already preparing or prepared")
	}
	preparationDone := make(chan struct{})
	s.state = stPreparing
	s.preparationDone = preparationDone
	s.lifecycleMu.Unlock()
	var committed bool
	var lease *epochLease
	defer func() {
		if !committed {
			err = s.rollbackPreparation(ctx, lease, err)
		}
		close(preparationDone)
	}()

	app, ok := kratos.FromContext(ctx)
	if !ok {
		return errors.New("node: application context is required")
	}
	if !locate.ValidServiceName(app.Name()) || app.ID() == "" {
		return errors.New("node: invalid application identity")
	}
	if _, endpointErr := s.resolveGRPCEndpoint(ctx); endpointErr != nil {
		return endpointErr
	}
	identity, lease, err := s.claimNodeIdentity(ctx, app)
	if err != nil {
		return err
	}
	s.lifecycleMu.Lock()
	if s.state != stPreparing || s.requests.isClosed() {
		s.lifecycleMu.Unlock()
		return errors.New("node: server is stopping or stopped")
	}
	s.lease.Store(lease)
	s.identity.Store(&identity)
	s.state = stPrepared
	committed = true
	handlerCount := len(s.handlers)
	s.lifecycleMu.Unlock()
	slog.InfoContext(ctx, "node prepared",
		"service", app.Name(),
		"version", app.Version(),
		"node_id", app.ID(),
		"pid", os.Getpid(),
		"sticky", lease != nil,
		"handlers", handlerCount,
		"push_timeout", s.pushTimeout.String(),
	)
	return nil
}

func (s *Server) rollbackPreparation(ctx context.Context, lease *epochLease, cause error) error {
	s.beginStopping()
	cause = errors.Join(cause, s.closeGRPCListener())
	if lease == nil {
		return cause
	}
	s.lifecycleMu.Lock()
	s.lease.Store(lease)
	s.lifecycleMu.Unlock()
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(normalizeContext(ctx)), s.pushTimeout)
	defer cancel()
	if err := lease.release(cleanupCtx); err != nil {
		return errors.Join(cause, fmt.Errorf("node: roll back epoch: %w", err))
	}
	return cause
}

// rollbackStart 使用独立的有界 context，按完整 Stop 顺序清理启动失败。
func (s *Server) rollbackStart(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(normalizeContext(ctx)), s.pushTimeout)
	defer cancel()
	return s.Stop(ctx)
}

func (s *Server) failLifecycle(err error) {
	s.deliveries.close()
	s.beginStopping()
	s.fatalCancel(err)
}

func (s *Server) beginStopping() <-chan struct{} {
	s.requests.close()
	s.lifecycleMu.Lock()
	preparationDone := s.preparationDone
	s.lifecycleMu.Unlock()
	return preparationDone
}

func (s *Server) claimNodeIdentity(ctx context.Context, app kratos.AppInfo) (nodeIdentity, *epochLease, error) {
	sticky, err := instance.IsSticky(app.Metadata())
	if err != nil {
		return nodeIdentity{}, nil, fmt.Errorf("node: %w", err)
	}
	if sticky != (s.locator != nil) {
		return nodeIdentity{}, nil, errors.New("node: sticky metadata and Node locator mismatch")
	}
	identity := nodeIdentity{serviceName: app.Name(), nodeID: app.ID()}
	if s.locator == nil {
		return identity, nil, nil
	}
	if err = s.locator.Ping(ctx); err != nil {
		return nodeIdentity{}, nil, err
	}
	lease, err := claimEpoch(ctx, s.locator, identity)
	if err != nil {
		return nodeIdentity{}, lease, err
	}
	return lease.identity, lease, nil
}

// releaseEpoch 先撤下服务身份，再停止续租并释放独立保存的凭据。
func (s *Server) releaseEpoch(ctx context.Context) error {
	lease := s.lease.Load()
	s.lifecycleMu.Lock()
	s.identity.Store(nil)
	s.lifecycleMu.Unlock()
	return lease.release(normalizeContext(ctx))
}
