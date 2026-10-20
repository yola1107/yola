package node

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"yola/instance"
	"yola/internal/contextwait"
	"yola/locate"

	"github.com/go-kratos/kratos/v3"
	"github.com/go-kratos/kratos/v3/registry"
	"google.golang.org/grpc"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

var (
	errNodeStopped      = errors.New("node: server is stopping or stopped")
	errRegistrationLost = errors.New("node: registration lease lost")
)

// Start 拥有内部监听、epoch 和目录登记；全部就绪后才开放请求。
func (s *Server) Start(ctx context.Context) error {
	ctx = normalizeContext(ctx)
	s.lifecycleMu.Lock()
	if s.requests.isClosed() {
		s.lifecycleMu.Unlock()
		return errNodeStopped
	}
	if s.state != _stNew {
		state := s.state
		s.lifecycleMu.Unlock()
		if state == _stStarted {
			return errors.New("node: server is already started")
		}
		return errors.New("node: server is already starting")
	}
	startupCtx, cancel := context.WithCancel(ctx)
	s.startupCancel = cancel
	s.lifecycleCtx = context.WithoutCancel(ctx)
	s.startupDone = make(chan struct{})
	s.state = _stStarting
	s.lifecycleMu.Unlock()

	serveResult, err := s.start(startupCtx, ctx)
	cancel()
	close(s.startupDone)
	if err != nil {
		return s.stopAfterFailure(ctx, errors.Join(context.Cause(s.fatalCtx), err), s.cleanupTimeout)
	}
	registrationDone := s.registrationDone()
	for {
		select {
		case <-registrationDone:
			registrationDone = nil
			// 正常 Stop 会主动注销；只有仍在服务时失租才触发 fatal。
			if !s.requests.isClosed() {
				s.failLifecycle(errRegistrationLost)
			}
		case <-s.fatalCtx.Done():
			return s.stopAfterFailure(ctx, context.Cause(s.fatalCtx), s.shutdownTimeout)
		case serveErr := <-serveResult:
			if fatalErr := context.Cause(s.fatalCtx); fatalErr != nil {
				return s.stopAfterFailure(ctx, fatalErr, s.shutdownTimeout)
			}
			if !s.requests.isClosed() {
				if serveErr == nil {
					serveErr = errors.New("node: gRPC server stopped unexpectedly")
				}
				return s.stopAfterFailure(ctx, serveErr, s.shutdownTimeout)
			}
			if errors.Is(serveErr, grpc.ErrServerStopped) {
				return nil
			}
			return serveErr
		}
	}
}

// Stop 只触发一次回收；调用者超时不改变回收进程和其他调用者的等待结果。
func (s *Server) Stop(ctx context.Context) error {
	ctx = normalizeContext(ctx)
	s.startCleanup(ctx, s.shutdownTimeout)
	return s.waitCleanup(ctx)
}

func (s *Server) failLifecycle(err error) {
	s.beginStopping()
	s.deliveries.close()
	s.fatalCancel(err)
	s.lifecycleMu.Lock()
	timeout := s.shutdownTimeout
	if s.state != _stStarted {
		timeout = s.cleanupTimeout
	}
	s.lifecycleMu.Unlock()
	s.startCleanup(context.Background(), timeout)
}

func (s *Server) start(ctx, serveCtx context.Context) (<-chan error, error) {
	app, ok := kratos.FromContext(ctx)
	if !ok {
		return nil, errors.New("node: application context is required")
	}
	if !locate.ValidServiceName(app.Name()) || app.ID() == "" {
		return nil, errors.New("node: invalid application identity")
	}
	endpoint, err := s.resolveGRPCEndpoint(ctx)
	if err != nil {
		return nil, err
	}
	identity, lease, err := s.claimNodeIdentity(ctx, app)
	// 即使申请迟到，固定凭据也交给唯一回收 owner。
	s.lease.Store(lease)
	if err != nil {
		return nil, err
	}
	if err = s.startupError(ctx); err != nil {
		return nil, err
	}
	s.identity.Store(&identity)
	if ready := lease.start(s.failLifecycle); ready != nil {
		select {
		case err = <-ready:
		case <-ctx.Done():
			err = ctx.Err()
		}
	}
	if err == nil {
		err = lease.valid()
	}
	if err != nil {
		return nil, err
	}
	if err = s.startupError(ctx); err != nil {
		return nil, err
	}
	serveResult, err := s.startGRPC(ctx, serveCtx)
	if err != nil {
		return nil, err
	}
	if err = s.startupError(ctx); err != nil {
		return nil, err
	}
	record := s.registrationRecord(app, endpoint)
	if err = s.registrar.Register(ctx, record); err != nil {
		return nil, err
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.registration = record
	if s.requests.isClosed() {
		return nil, errNodeStopped
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = lease.valid(); err != nil {
		return nil, err
	}
	select {
	case <-s.registrationDone():
		return nil, errRegistrationLost
	default:
	}
	s.state = _stStarted
	s.deliveries.open()
	s.requests.open()
	s.healthServer.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	return serveResult, nil
}

func (s *Server) startGRPC(ctx, serveCtx context.Context) (<-chan error, error) {
	serveResult := make(chan error, 1)
	go func() { serveResult <- s.grpcServer.Start(serveCtx) }()
	select {
	case <-s.grpcListener.Accepting():
		return serveResult, nil
	case serveErr := <-serveResult:
		if serveErr == nil {
			serveErr = errors.New("node: gRPC server stopped during startup")
		}
		return nil, serveErr
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *Server) registrationDone() <-chan struct{} {
	if lifetime, ok := s.registrar.(interface{ Done() <-chan struct{} }); ok {
		return lifetime.Done()
	}
	return nil
}

func (s *Server) startCleanup(base context.Context, timeout time.Duration) {
	s.beginStopping()
	s.cleanupOnce.Do(func() {
		s.lifecycleMu.Lock()
		if s.lifecycleCtx != nil {
			base = s.lifecycleCtx
		}
		s.lifecycleMu.Unlock()
		go func() {
			s.cleanupErr = s.cleanup(base, timeout)
			close(s.cleanupDone)
		}()
	})
}

func (s *Server) cleanup(base context.Context, timeout time.Duration) error {
	base = context.WithoutCancel(base)
	ctx, cancel := context.WithTimeout(base, timeout)
	defer cancel()
	s.lifecycleMu.Lock()
	startupDone := s.startupDone
	s.lifecycleMu.Unlock()
	// 为在途启动保留一部分预算，不能让它耗尽网络与业务回收的时间。
	var startupErr error
	if startupDone != nil {
		waitCtx, waitCancel := context.WithTimeout(ctx, timeout/4)
		startupErr = contextwait.Done(waitCtx, startupDone)
		waitCancel()
	}
	record := s.registeredInstance()
	registrationErr := s.deregister(ctx, record, timeout/4)
	requestErr := s.requests.stopAndWait(ctx)
	var drainErr error
	var lateDrain <-chan error
	if requestErr == nil && s.drain != nil {
		result := make(chan error, 1)
		go func() { result <- s.drain(ctx) }()
		select {
		case drainErr = <-result:
		case <-ctx.Done():
			drainErr = ctx.Err()
			lateDrain = result
		}
	}
	// Drain 停止绑定与推送生产者后，再关闭副作用准入。
	deliveryErr := s.deliveries.stopAndWait(ctx)
	drained := requestErr == nil && drainErr == nil && deliveryErr == nil
	lease := s.lease.Load()
	var renewalErr error
	if !drained || startupErr != nil {
		renewalErr = lease.stopRenewal(ctx)
	}
	networkErr := errors.Join(s.grpcServer.Stop(ctx), s.grpcListener.Close(), s.gateways.Close())
	s.identity.Store(nil)
	var epochErr error
	if drained && startupErr == nil {
		epochErr = lease.release(ctx)
	}
	cleanupErr := errors.Join(startupErr, registrationErr, requestErr, drainErr, deliveryErr, renewalErr, networkErr, epochErr)
	if startupErr != nil {
		// 创建者交接的迟到资源先回收，不能被尚未返回的 Drain 阻塞。
		<-startupDone
		lateCtx, lateCancel := context.WithTimeout(base, timeout)
		if lateRecord := s.registeredInstance(); lateRecord != record {
			cleanupErr = errors.Join(cleanupErr, s.deregister(lateCtx, lateRecord, timeout/4))
		}
		if drained {
			epochErr = s.lease.Load().release(lateCtx)
		} else {
			epochErr = s.lease.Load().stopRenewal(lateCtx)
		}
		lateCancel()
		s.identity.Store(nil)
		cleanupErr = errors.Join(cleanupErr, epochErr)
	}
	// 已知网络和续租已关闭；迟到的 Drain 仍由本轮回收汇合，不能重复执行。
	if lateDrain != nil {
		cleanupErr = errors.Join(cleanupErr, <-lateDrain)
	}
	return cleanupErr
}

func (s *Server) startupError(ctx context.Context) error {
	if s.requests.isClosed() {
		return errNodeStopped
	}
	return ctx.Err()
}

func (s *Server) resolveGRPCEndpoint(ctx context.Context) (*url.URL, error) {
	if err := s.grpcListener.Prepare(ctx); err != nil {
		return nil, fmt.Errorf("node: prepare gRPC listener: %w", err)
	}
	endpoint, err := s.grpcServer.Endpoint()
	if err != nil {
		return nil, fmt.Errorf("node: resolve gRPC endpoint: %w", err)
	}
	return endpoint, nil
}

func (s *Server) claimNodeIdentity(ctx context.Context, app kratos.AppInfo) (nodeIdentity, *epochLease, error) {
	identity := nodeIdentity{serviceName: app.Name(), nodeID: app.ID()}
	if s.locator == nil {
		return identity, nil, nil
	}
	if err := s.locator.Ping(ctx); err != nil {
		return nodeIdentity{}, nil, err
	}
	lease, err := claimEpoch(ctx, s.locator, identity)
	if err != nil {
		return nodeIdentity{}, lease, err
	}
	return lease.identity, lease, nil
}

func (s *Server) registrationRecord(app kratos.AppInfo, endpoint *url.URL) *registry.ServiceInstance {
	var metadata map[string]string
	if s.locator != nil {
		metadata = instance.StickyMetadata()
	}
	return &registry.ServiceInstance{
		ID:        app.ID(),
		Name:      app.Name(),
		Version:   app.Version(),
		Metadata:  metadata,
		Endpoints: []string{endpoint.String()},
	}
}

func (s *Server) beginStopping() {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.requests.close()
	s.healthServer.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
	if s.startupCancel != nil {
		s.startupCancel()
	}
}

func (s *Server) registeredInstance() *registry.ServiceInstance {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	return s.registration
}

func (s *Server) deregister(ctx context.Context, record *registry.ServiceInstance, timeout time.Duration) error {
	if record == nil {
		return nil
	}
	// 注销失败不能耗尽请求、Drain、网络与 epoch 的回收预算。
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- s.registrar.Deregister(ctx, record) }()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Server) stopAfterFailure(ctx context.Context, cause error, timeout time.Duration) error {
	s.startCleanup(ctx, timeout)
	waitCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return errors.Join(cause, s.waitCleanup(waitCtx))
}

func (s *Server) waitCleanup(ctx context.Context) error {
	if err := contextwait.Done(ctx, s.cleanupDone); err != nil {
		return err
	}
	return s.cleanupErr
}
