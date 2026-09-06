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
	grpcgo "google.golang.org/grpc"
)

// Start begins runtime work and serves resources prepared by BeforeStart.
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
	s.lease.start(s.failLifecycle)
	s.state = stStarted
	s.lifecycleMu.Unlock()

	result := make(chan error, 1)
	go func() { result <- s.grpcServer.Start(ctx) }()
	select {
	case <-s.fatalCtx.Done():
		return context.Cause(s.fatalCtx)
	case err := <-result:
		if fatalErr := context.Cause(s.fatalCtx); fatalErr != nil {
			return fatalErr
		}
		if errors.Is(err, grpcgo.ErrServerStopped) {
			return nil
		}
		if err != nil {
			return errors.Join(err, s.rollbackStart(ctx))
		}
		return nil
	}
}

// Stop rejects new requests, drains accepted work, and releases the epoch only after a successful drain.
func (s *Server) Stop(ctx context.Context) error {
	ctx = normalizeContext(ctx)
	firstStop := false
	var epochErr error
	s.stopOnce.Do(func() {
		firstStop = true
		s.stopErr, epochErr = s.shutdown(ctx)
	})
	if !firstStop {
		epochErr = s.currentLease().retryRelease(ctx)
	}
	return errors.Join(s.stopErr, epochErr)
}

func (s *Server) shutdown(ctx context.Context) (error, error) {
	preparationDone := s.beginStopping()
	if preparationDone != nil {
		if err := contextwait.Done(ctx, preparationDone); err != nil {
			return err, nil
		}
	}

	requestErr := s.requests.stopAndWait(ctx)
	var drainErr error
	if requestErr == nil && s.drain != nil {
		drainErr = s.drain(ctx)
	}
	if requestErr != nil || drainErr != nil {
		s.currentLease().stopRenewal()
	}
	grpcErr := errors.Join(s.grpcServer.Stop(ctx), s.closeGRPCListener())
	var epochErr error
	if requestErr == nil && drainErr == nil {
		epochErr = s.releaseEpoch(ctx)
	}
	slog.InfoContext(ctx, "node stopping", "pid", os.Getpid())
	return errors.Join(requestErr, drainErr, grpcErr, s.gateways.Close()), epochErr
}

// BeforeStart validates the Kratos identity, verifies Locator access, and claims the Node epoch.
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
	committed := false
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
	s.identity.Store(&identity)
	s.lease = lease
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
	s.lease = lease
	s.lifecycleMu.Unlock()
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(normalizeContext(ctx)), s.pushTimeout)
	defer cancel()
	if err := lease.release(cleanupCtx); err != nil {
		return errors.Join(cause, fmt.Errorf("node: roll back epoch: %w", err))
	}
	return cause
}

// rollbackStart releases resources prepared before a failed gRPC Start.
func (s *Server) rollbackStart(ctx context.Context) error {
	ctx = normalizeContext(ctx)
	s.beginStopping()
	return errors.Join(s.releaseEpoch(ctx), s.closeGRPCListener())
}

func (s *Server) failLifecycle(err error) {
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
		return nodeIdentity{}, nil, err
	}
	return lease.identity, lease, nil
}

func (s *Server) currentLease() *epochLease {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	return s.lease
}

// releaseEpoch 先停止续租并撤下服务身份，再释放独立保存的租约凭据。
func (s *Server) releaseEpoch(ctx context.Context) error {
	lease := s.currentLease()
	lease.stopRenewal()
	s.lifecycleMu.Lock()
	s.identity.Store(nil)
	s.lifecycleMu.Unlock()
	return lease.release(normalizeContext(ctx))
}
