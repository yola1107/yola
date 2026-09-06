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
	"time"

	protocolv1 "yola/api/protocol/v1"
	"yola/internal/contextwait"
	"yola/locate"

	"github.com/go-kratos/kratos/v3"
	grpcgo "google.golang.org/grpc"
)

// Endpoint returns the internal gRPC endpoint published through the Kratos registry.
func (s *Server) Endpoint() (*url.URL, error) {
	s.lifecycle.mu.Lock()
	defer s.lifecycle.mu.Unlock()
	if s.lifecycle.state == stStopping {
		return nil, errors.New("gateway: server is stopping or stopped")
	}
	return s.resolveGRPCEndpoint(context.Background())
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

// BeforeStart validates the Kratos identity and prepares external resources before registration.
func (s *Server) BeforeStart(ctx context.Context) (err error) {
	s.lifecycle.mu.Lock()
	switch s.lifecycle.state {
	case stStopping:
		s.lifecycle.mu.Unlock()
		return errors.New("gateway: server is stopping or stopped")
	case stPreparing:
		s.lifecycle.mu.Unlock()
		return errors.New("gateway: server is already preparing or prepared")
	case stPrepared, stStarted:
		s.lifecycle.state = stStopping
		s.lifecycle.mu.Unlock()
		return errors.New("gateway: server is already preparing or prepared")
	}
	preparationDone := make(chan struct{})
	s.lifecycle.state = stPreparing
	s.lifecycle.preparationDone = preparationDone
	s.lifecycle.mu.Unlock()
	committed := false
	transportAttempts := 0
	defer func() {
		if !committed {
			err = s.rollbackPreparation(err, transportAttempts)
		}
		close(preparationDone)
	}()
	app, ok := kratos.FromContext(ctx)
	if validationErr := validateApplication(app, ok); validationErr != nil {
		return validationErr
	}

	// App endpoints aggregate every transport; GateBinding must identify this Gateway's gRPC server.
	resolved, err := s.resolveGRPCEndpoint(ctx)
	if err != nil {
		return err
	}
	if resolved == nil || resolved.Host == "" ||
		(resolved.Scheme != "grpc" && resolved.Scheme != "grpcs") {
		return errors.New("gateway: valid gRPC application endpoint is required")
	}
	endpoint := resolved.String()
	pingCtx, stopPing := context.WithTimeout(ctx, s.rpcTimeout)
	err = s.locator.Ping(pingCtx)
	stopPing()
	if err != nil {
		return err
	}
	preparedIdentity := identity{id: app.ID(), endpoint: endpoint}
	if err = s.broadcaster.start(); err != nil {
		return err
	}
	for index, clientTransport := range s.transports {
		transportAttempts = index + 1
		if err := clientTransport.BeforeStart(ctx); err != nil {
			return fmt.Errorf("gateway: prepare client transport %d (%T): %w", index, clientTransport, err)
		}
	}
	s.lifecycle.mu.Lock()
	if s.lifecycle.state != stPreparing {
		s.lifecycle.mu.Unlock()
		return errors.New("gateway: server is stopping or stopped")
	}
	s.identity = preparedIdentity
	s.lifecycle.state = stPrepared
	committed = true
	s.lifecycle.mu.Unlock()
	slog.InfoContext(ctx, "gateway prepared",
		"service", app.Name(),
		"version", app.Version(),
		"gate_id", app.ID(),
		"pid", os.Getpid(),
		"endpoint", endpoint,
		"rpc_timeout", s.rpcTimeout.String(),
		"auth_timeout", s.authTimeout.String(),
		"lease_ttl", s.leaseTTL.String(),
	)
	return nil
}

func (s *Server) rollbackPreparation(cause error, transportAttempts int) error {
	stop := func(stopResource func(context.Context) error) error {
		ctx, cancel := context.WithTimeout(context.Background(), s.rpcTimeout)
		defer cancel()
		return stopResource(ctx)
	}
	for index := transportAttempts - 1; index >= 0; index-- {
		if err := stop(s.transports[index].Stop); err != nil {
			cause = errors.Join(cause, fmt.Errorf(
				"gateway: roll back client transport %d (%T): %w",
				index,
				s.transports[index],
				err,
			))
		}
	}
	cause = errors.Join(cause, stop(s.broadcaster.stop))
	cause = errors.Join(cause, s.closeGRPCListener())
	s.lifecycle.mu.Lock()
	s.lifecycle.state = stStopping
	s.identity = identity{}
	s.lifecycle.mu.Unlock()
	return cause
}

func (s *Server) quiesce(ctx context.Context) error {
	s.lifecycle.mu.Lock()
	firstStop := s.lifecycle.state != stStopping
	s.lifecycle.state = stStopping
	s.lifecycle.mu.Unlock()

	s.admission.mu.Lock()
	s.admission.accepting.Store(false)
	s.admission.mu.Unlock()
	if firstStop {
		slog.InfoContext(ctx, "gateway stopping")
	}
	if err := waitGroupContext(ctx, &s.admission.wg); err != nil {
		go func() {
			s.admission.wg.Wait()
			_ = s.gateways.Close()
		}()
		return err
	}
	_ = s.gateways.Close()
	return nil
}

func (s *Server) waitForPreparation(ctx context.Context) error {
	s.lifecycle.mu.Lock()
	preparationDone := s.lifecycle.preparationDone
	stoppingPreparation := s.lifecycle.state == stPreparing
	if stoppingPreparation {
		s.lifecycle.state = stStopping
	}
	s.lifecycle.mu.Unlock()
	if preparationDone == nil {
		return nil
	}
	if stoppingPreparation {
		slog.InfoContext(ctx, "gateway stopping")
	}
	return contextwait.Done(ctx, preparationDone)
}

func (s *Server) admitAuthentication(ctx context.Context, sess *session) bool {
	s.admission.mu.Lock()
	defer s.admission.mu.Unlock()
	if !s.admission.accepting.Load() || !sess.canAuthenticate(ctx, time.Now()) ||
		!s.sessions.current(sess) {
		return false
	}
	s.admission.wg.Add(1)
	return true
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

// Start activates runtime work and serves resources prepared by BeforeStart.
func (s *Server) Start(ctx context.Context) error {
	s.lifecycle.mu.Lock()
	switch s.lifecycle.state {
	case stStopping:
		s.lifecycle.mu.Unlock()
		return errors.New("gateway: server is stopping or stopped")
	case stStarted:
		s.lifecycle.mu.Unlock()
		return errors.New("gateway: server is already started")
	case stPrepared:
	default:
		s.lifecycle.mu.Unlock()
		return errors.New("gateway: server is not prepared")
	}
	s.lifecycle.state = stStarted
	s.admission.accepting.Store(true)
	s.lifecycle.mu.Unlock()

	serverCount := len(s.transports) + 1
	results := make(chan error, serverCount)
	for index, clientTransport := range s.transports {
		go func() {
			err := clientTransport.Start(ctx)
			if err != nil {
				err = fmt.Errorf("gateway: start client transport %d (%T): %w", index, clientTransport, err)
			}
			results <- err
		}()
	}
	go func() {
		err := s.grpcServer.Start(ctx)
		if errors.Is(err, grpcgo.ErrServerStopped) {
			err = nil
		} else if err != nil {
			err = fmt.Errorf("gateway: start gRPC transport: %w", err)
		}
		results <- err
	}()

	for range serverCount {
		if err := <-results; err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.lifecycle.stopOnce.Do(func() {
		s.lifecycle.stopErr = s.shutdown(ctx)
	})
	return s.lifecycle.stopErr
}

func (s *Server) shutdown(ctx context.Context) error {
	if err := s.waitForPreparation(ctx); err != nil {
		return err
	}
	quiesceErr := s.quiesce(ctx)
	broadcastErr := s.broadcaster.stop(ctx)
	var drainErr error
	if ctx.Err() == nil && stopCompleted(broadcastErr) {
		drainErr = s.drainSessions(ctx)
	}
	var transportErr error
	for index, clientTransport := range s.transports {
		if err := clientTransport.Stop(ctx); err != nil {
			transportErr = errors.Join(transportErr, fmt.Errorf(
				"gateway: stop client transport %d (%T): %w",
				index,
				clientTransport,
				err,
			))
		}
	}
	if err := s.grpcServer.Stop(ctx); err != nil {
		transportErr = errors.Join(transportErr, fmt.Errorf("gateway: stop gRPC transport: %w", err))
	}
	transportErr = errors.Join(transportErr, s.closeGRPCListener())
	s.backends.close()
	return errors.Join(quiesceErr, broadcastErr, drainErr, transportErr)
}

func stopCompleted(err error) bool {
	// Stop implementations return context errors when their background work may still be running.
	return !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}

// drainSessions sends OpKick before closing so clients see a shutdown reason.
func (s *Server) drainSessions(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	sessions := s.sessions.takeAll()
	if len(sessions) == 0 {
		return nil
	}
	workerCount := min(shutdownWorkerCount, len(sessions))
	var next atomic.Uint64
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for {
				if ctx.Err() != nil {
					return
				}
				index := int(next.Add(1) - 1)
				if index >= len(sessions) {
					return
				}
				s.drainSession(ctx, sessions[index])
			}
		}()
	}
	return waitGroupContext(ctx, &workers)
}

func (s *Server) drainSession(ctx context.Context, sess *session) {
	ctx, cancel := context.WithTimeout(ctx, s.rpcTimeout)
	defer cancel()
	connID := sess.conn.ConnID()
	binding := sess.detachForClose()
	err := sess.conn.CloseWithProto(ctx, &protocolv1.Proto{
		Op: protocolv1.OpKick, Code: protocolv1.KickCodeServerShutdown,
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

// cleanupContext lets disconnect cleanup survive transport cancellation while one
// RPCTimeout bounds the complete cleanup sequence.
func (s *Server) cleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), s.rpcTimeout)
}
