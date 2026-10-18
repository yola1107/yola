package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"sync"
	"sync/atomic"

	"yola/api/protocol/v1"
	"yola/internal/contextwait"
	"yola/locate"

	"github.com/go-kratos/kratos/v3"
	"google.golang.org/grpc"
)

const _shutdownWorkerCount = 64

type lifecycleState uint8

const (
	_stNew lifecycleState = iota
	_stStarting
	_stStarted
	_stStopping
	_stStopped
)

// lifecycle 协调进程启动和停止，不参与单连接热路径。
type lifecycle struct {
	mu            sync.Mutex
	state         lifecycleState
	startupDone   chan struct{}
	startupCancel context.CancelFunc
	prepared      int
	serving       bool
	workers       sync.WaitGroup
	shutdownOnce  sync.Once
	shutdownDone  chan struct{}
	shutdownErr   error
}

// Start 准备全部依赖，内部 RPC 接收请求后才开放客户端准入。
func (s *Server) Start(ctx context.Context) (err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.lifecycle.mu.Lock()
	switch s.lifecycle.state {
	case _stStopping, _stStopped:
		s.lifecycle.mu.Unlock()
		return errors.New("gateway: server is stopping or stopped")
	case _stStarting, _stStarted:
		s.lifecycle.mu.Unlock()
		return errors.New("gateway: server is already starting or started")
	}
	runCtx, cancel := context.WithCancel(ctx)
	startupDone := make(chan struct{})
	s.lifecycle.state = _stStarting
	s.lifecycle.startupDone = startupDone
	s.lifecycle.startupCancel = cancel
	s.lifecycle.mu.Unlock()
	handoff := sync.OnceFunc(func() { close(startupDone) })
	defer func() {
		handoff()
		s.beginShutdown()
		<-s.lifecycle.shutdownDone
		err = errors.Join(err, s.lifecycle.shutdownErr)
	}()

	if err := s.prepare(runCtx); err != nil {
		return err
	}
	grpcResult := make(chan error, 1)
	s.lifecycle.workers.Go(func() {
		err := s.grpcServer.Start(runCtx)
		if errors.Is(err, grpc.ErrServerStopped) {
			err = nil
		} else if err != nil {
			err = fmt.Errorf("gateway: start gRPC transport: %w", err)
		}
		grpcResult <- err
	})
	select {
	case <-s.grpcListener.Accepting():
	case err := <-grpcResult:
		if err == nil {
			err = errors.New("gateway: gRPC transport stopped before accepting")
		}
		return err
	case <-runCtx.Done():
		return runCtx.Err()
	}
	s.lifecycle.mu.Lock()
	if s.lifecycle.state != _stStarting || runCtx.Err() != nil {
		s.lifecycle.mu.Unlock()
		return errors.New("gateway: server is stopping or stopped")
	}
	s.lifecycle.state = _stStarted
	s.lifecycle.serving = true
	s.sessions.openAdmission()
	clientResults := make(chan error, len(s.transports))
	for index, clientTransport := range s.transports {
		s.lifecycle.workers.Go(func() {
			err := clientTransport.Start(runCtx)
			if err != nil {
				err = fmt.Errorf("gateway: start client transport %d (%T): %w", index, clientTransport, err)
			}
			clientResults <- err
		})
	}
	s.lifecycle.mu.Unlock()
	handoff()

	for {
		select {
		case err := <-grpcResult:
			return err
		case err := <-clientResults:
			if err != nil {
				return err
			}
		case <-runCtx.Done():
			return nil
		}
	}
}

// Stop 只限制本次等待；后台清理始终由同一所有者完成。
func (s *Server) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.beginShutdown()
	if err := contextwait.Done(ctx, s.lifecycle.shutdownDone); err != nil {
		return err
	}
	return s.lifecycle.shutdownErr
}

func (s *Server) prepare(ctx context.Context) error {
	app, ok := kratos.FromContext(ctx)
	if err := validateApplication(app, ok); err != nil {
		return err
	}
	// App endpoints 汇集所有 transport；GateBinding 只使用本 Gateway 的内部地址。
	endpoint, err := s.resolveGRPCEndpoint(ctx)
	if err != nil {
		return err
	}
	if endpoint == nil || endpoint.Host == "" ||
		(endpoint.Scheme != "grpc" && endpoint.Scheme != "grpcs") {
		return errors.New("gateway: valid gRPC application endpoint is required")
	}
	pingCtx, cancel := context.WithTimeout(ctx, s.connectTimeout)
	err = s.locator.Ping(pingCtx)
	cancel()
	if err != nil {
		return err
	}
	if err := s.broadcaster.start(); err != nil {
		return err
	}
	for index, clientTransport := range s.transports {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := clientTransport.BeforeStart(ctx)
		s.lifecycle.mu.Lock()
		s.lifecycle.prepared = index + 1
		s.lifecycle.mu.Unlock()
		if err != nil {
			return fmt.Errorf("gateway: prepare client transport %d (%T): %w", index, clientTransport, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.identity = identity{id: app.ID(), endpoint: endpoint.String()}
	slog.InfoContext(ctx, "gateway prepared",
		"service", app.Name(),
		"version", app.Version(),
		"gate_id", app.ID(),
		"pid", os.Getpid(),
		"endpoint", endpoint.String(),
		"rpc_timeout", s.rpcTimeout.String(),
		"connect_timeout", s.connectTimeout.String(),
		"lease_timeout", s.leaseTimeout.String(),
		"cleanup_timeout", s.cleanupTimeout.String(),
		"shutdown_timeout", s.shutdownTimeout.String(),
		"auth_timeout", s.authTimeout.String(),
		"lease_ttl", s.leaseTTL.String(),
	)
	return nil
}

func (s *Server) resolveGRPCEndpoint(ctx context.Context) (*url.URL, error) {
	if err := s.grpcListener.Prepare(ctx); err != nil {
		return nil, fmt.Errorf("gateway: prepare gRPC listener: %w", err)
	}
	endpoint, err := s.grpcServer.Endpoint()
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("gateway: resolve gRPC endpoint: %w", err),
			s.closeGRPCListener(),
		)
	}
	return endpoint, nil
}

func (s *Server) closeGRPCListener() error {
	if err := s.grpcListener.Close(); err != nil {
		return fmt.Errorf("gateway: close gRPC listener: %w", err)
	}
	return nil
}

func validateApplication(app kratos.AppInfo, ok bool) error {
	switch {
	case !ok:
		return errors.New("gateway: application context is required")
	case !locate.ValidServiceName(app.Name()):
		return errors.New("gateway: invalid application name")
	case app.ID() == "":
		return errors.New("gateway: application ID is required")
	default:
		return nil
	}
}

func (s *Server) beginShutdown() {
	s.lifecycle.mu.Lock()
	if s.lifecycle.state != _stStopped {
		s.lifecycle.state = _stStopping
	}
	if s.lifecycle.startupCancel != nil {
		s.lifecycle.startupCancel()
	}
	s.sessions.closeAdmission()
	s.lifecycle.shutdownOnce.Do(func() {
		startupDone := s.lifecycle.startupDone
		go func() {
			slog.Info("gateway stopping")
			ctx, cancel := context.WithTimeout(context.Background(), s.shutdownTimeout)
			defer cancel()
			var preparationErr error
			var lateStops []<-chan error
			var stoppedTransports int
			// 初始化超时先关闭已交接的资源；当前准备中的 transport 仍归创建者。
			if startupDone != nil {
				select {
				case <-startupDone:
				case <-ctx.Done():
					s.lifecycle.mu.Lock()
					stoppedTransports = s.lifecycle.prepared
					s.lifecycle.mu.Unlock()
					preparationErr = errors.Join(s.closeGRPCListener(), s.broadcaster.stop(ctx))
					pending, stopErr := s.stopClientTransports(ctx, 0, stoppedTransports, true)
					preparationErr = errors.Join(preparationErr, stopErr)
					lateStops = pending
					<-startupDone
				}
			}
			err := errors.Join(preparationErr, s.shutdown(ctx, stoppedTransports))
			for _, result := range lateStops {
				err = errors.Join(err, <-result)
			}
			s.lifecycle.mu.Lock()
			s.lifecycle.shutdownErr = err
			s.lifecycle.state = _stStopped
			s.lifecycle.mu.Unlock()
			close(s.lifecycle.shutdownDone)
		}()
	})
	s.lifecycle.mu.Unlock()
}

func (s *Server) shutdown(ctx context.Context, stoppedTransports int) error {
	quiesceErr := s.quiesce(ctx)
	broadcastErr := s.broadcaster.stop(ctx)
	sessions := s.sessions.takeAll()
	drainDone := s.startSessionDrain(ctx, sessions)
	drainErr := contextwait.Done(ctx, drainDone)
	if drainErr != nil {
		// 排空锁仍由原路径释放；先关闭物理连接，避免超时阻止其余资源回收。
		for _, sess := range sessions {
			_ = sess.conn.Close()
		}
	}
	lateStops, transportErr := s.stopClientTransports(ctx, stoppedTransports, len(s.transports), !s.lifecycle.serving)
	grpcCtx := ctx
	cancel := func() {}
	if !s.lifecycle.serving {
		grpcCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), s.cleanupTimeout)
	}
	if err := s.grpcServer.Stop(grpcCtx); err != nil {
		transportErr = errors.Join(transportErr, fmt.Errorf("gateway: stop gRPC transport: %w", err))
	}
	cancel()
	transportErr = errors.Join(transportErr, s.closeGRPCListener())
	s.backends.close()
	// 网络已关闭；迟到的认证、推送与连接清理继续由本轮停机负责并汇合。
	s.sessions.authentications.Wait()
	_ = s.gateways.Close()
	_ = s.broadcaster.stop(context.Background())
	<-drainDone
	s.lifecycle.workers.Wait()
	for _, result := range lateStops {
		transportErr = errors.Join(transportErr, <-result)
	}
	return errors.Join(ctx.Err(), quiesceErr, broadcastErr, drainErr, transportErr)
}

func (s *Server) quiesce(ctx context.Context) error {
	s.sessions.closeAdmission()
	if err := waitGroupContext(ctx, &s.sessions.authentications); err != nil {
		return err
	}
	return s.gateways.Close()
}

// 停止某个 transport 超时后继续回收其余资源；迟到结果由同一 owner 最后汇合。
func (s *Server) stopClientTransports(ctx context.Context, first, end int, rollback bool) ([]<-chan error, error) {
	var stopErr error
	var pending []<-chan error
	for offset := range end - first {
		index := first + offset
		stopCtx := ctx
		cancel := func() {}
		action := "stop"
		if rollback {
			index = end - 1 - offset
			action = "roll back"
			stopCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), s.cleanupTimeout)
		}
		clientTransport := s.transports[index]
		wrap := func(err error) error {
			if err == nil {
				return nil
			}
			return fmt.Errorf("gateway: %s client transport %d (%T): %w", action, index, clientTransport, err)
		}
		result := make(chan error, 1)
		go func() {
			defer cancel()
			result <- wrap(clientTransport.Stop(stopCtx))
		}()
		select {
		case err := <-result:
			stopErr = errors.Join(stopErr, err)
		case <-stopCtx.Done():
			stopErr = errors.Join(stopErr, wrap(stopCtx.Err()))
			pending = append(pending, result)
		}
	}
	return pending, stopErr
}

func (s *Server) startSessionDrain(ctx context.Context, sessions []*session) <-chan struct{} {
	done := make(chan struct{})
	if len(sessions) == 0 {
		close(done)
		return done
	}
	workerCount := min(_shutdownWorkerCount, len(sessions))
	var next atomic.Uint64
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for {
				index := int(next.Add(1) - 1)
				if index >= len(sessions) {
					return
				}
				s.drainSession(ctx, sessions[index])
			}
		}()
	}
	go func() {
		workers.Wait()
		close(done)
	}()
	return done
}

func (s *Server) drainSession(ctx context.Context, sess *session) {
	ctx, cancel := context.WithTimeout(ctx, s.cleanupTimeout)
	defer cancel()
	connID := sess.conn.ConnID()
	binding := sess.detachForClose()
	err := sess.conn.CloseWithProto(ctx, &v1.Proto{
		Op:   v1.OpKick,
		Code: v1.KickCodeServerShutdown,
	})
	err = errors.Join(err, sess.conn.Close())
	if locate.ValidGateBinding(binding) && ctx.Err() == nil {
		s.unbindGate(ctx, binding)
		if ctx.Err() == nil {
			s.notifyDisconnect(ctx, binding)
		}
	}
	if err != nil {
		slog.WarnContext(ctx, "session shutdown kick failed",
			"conn_id", connID,
			"error", err,
		)
	}
}

func waitGroupContext(ctx context.Context, wg *sync.WaitGroup) error {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	return contextwait.Done(ctx, done)
}

// cleanupContext 使断线清理独立于 transport 取消，由 CleanupTimeout 限制整段清理。
func (s *Server) cleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), s.cleanupTimeout)
}
