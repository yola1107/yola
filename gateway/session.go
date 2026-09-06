package gateway

import (
	"context"
	"slices"
	"sync"
	"time"

	"yola/api/protocol/v1"
	"yola/locate"
	"yola/network"
)

type sessionRegistry struct {
	mu       sync.RWMutex
	byConnID map[string]*session
}

func (r *sessionRegistry) add(conn network.Connection, authTimeout time.Duration) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byConnID == nil || r.byConnID[conn.ConnID()] != nil {
		return false
	}
	r.byConnID[conn.ConnID()] = newSession(conn, authTimeout)
	return true
}

func (r *sessionRegistry) get(connID string) *session {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byConnID[connID]
}

func (r *sessionRegistry) current(sess *session) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byConnID != nil && r.byConnID[sess.conn.ConnID()] == sess
}

func (r *sessionRegistry) commitAuthentication(ctx context.Context, sess *session, lease locate.GateLease, boundAt time.Time) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.byConnID == nil || r.byConnID[sess.conn.ConnID()] != sess ||
		!sess.canAuthenticate(ctx, time.Now()) {
		return false
	}
	// Holding the registry read lock orders the local commit against Close removal.
	return sess.finishAuthentication(lease, boundAt)
}

func (r *sessionRegistry) remove(conn network.Connection) *session {
	r.mu.Lock()
	defer r.mu.Unlock()
	sess := r.byConnID[conn.ConnID()]
	if sess == nil || sess.conn != conn {
		return nil
	}
	delete(r.byConnID, conn.ConnID())
	return sess
}

func (r *sessionRegistry) takeAll() []*session {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byConnID == nil {
		return nil
	}
	sessions := make([]*session, 0, len(r.byConnID))
	for _, sess := range r.byConnID {
		sessions = append(sessions, sess)
	}
	r.byConnID = nil
	return sessions
}

func (r *sessionRegistry) snapshot(sessions []*session) []*session {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.byConnID == nil {
		return sessions[:0]
	}
	sessions = slices.Grow(sessions[:0], len(r.byConnID))
	for _, sess := range r.byConnID {
		sessions = append(sessions, sess)
	}
	return sessions
}

type session struct {
	// handlerMu serializes Handle and Close. It may be held while bindingMu is
	// acquired; the reverse lock order is forbidden.
	handlerMu     sync.Mutex
	bindingMu     sync.Mutex
	conn          network.Connection
	authTimer     *time.Timer
	authDeadline  time.Time
	authAttempted bool
	binding       locate.GateBinding
	leaseDeadline time.Time
}

func newSession(conn network.Connection, authTimeout time.Duration) *session {
	sess := &session{conn: conn, authDeadline: time.Now().Add(authTimeout)}
	sess.authTimer = time.AfterFunc(authTimeout, func() {
		if !sess.authenticated() {
			_ = sess.conn.Close()
		}
	})
	return sess
}

// leaseValidLocked requires bindingMu to be held.
func (s *session) leaseValidLocked(now time.Time) bool {
	return locate.ValidGateBinding(s.binding) && now.Before(s.leaseDeadline)
}

func (s *session) authenticated() bool {
	s.bindingMu.Lock()
	defer s.bindingMu.Unlock()
	return s.leaseValidLocked(time.Now())
}

func (s *session) beginAuthentication() bool {
	if s.authAttempted {
		return false
	}
	s.authAttempted = true
	return true
}

func (s *session) canAuthenticate(ctx context.Context, now time.Time) bool {
	return ctx.Err() == nil && now.Before(s.authDeadline)
}

func (s *session) finishAuthentication(lease locate.GateLease, boundAt time.Time) bool {
	s.bindingMu.Lock()
	now := time.Now()
	leaseDeadline := boundAt.Add(lease.TTL)
	if !now.Before(s.authDeadline) || !now.Before(leaseDeadline) {
		s.bindingMu.Unlock()
		return false
	}
	s.binding = lease.Binding
	s.leaseDeadline = leaseDeadline
	authTimer := s.authTimer
	s.authTimer = nil
	s.bindingMu.Unlock()
	if authTimer != nil {
		authTimer.Stop()
	}
	return true
}

func (s *session) route(now time.Time) (locate.GateBinding, bool) {
	s.bindingMu.Lock()
	defer s.bindingMu.Unlock()
	return s.binding, s.leaseValidLocked(now)
}

func (s *session) heartbeatRoute(now time.Time, interval time.Duration) (locate.GateBinding, bool, bool) {
	s.bindingMu.Lock()
	defer s.bindingMu.Unlock()
	valid := s.leaseValidLocked(now)
	return s.binding, valid, valid && !now.Add(interval).Before(s.leaseDeadline)
}

func (s *session) finishHeartbeat(expected locate.GateBinding, renewedAt time.Time, ttl time.Duration) {
	s.bindingMu.Lock()
	if s.binding == expected {
		s.leaseDeadline = renewedAt.Add(ttl)
	}
	s.bindingMu.Unlock()
}

func (s *session) detachForClose() locate.GateBinding {
	s.handlerMu.Lock()
	defer s.handlerMu.Unlock()
	s.bindingMu.Lock()
	binding, authTimer := s.binding, s.authTimer
	s.authTimer = nil
	s.binding = locate.GateBinding{}
	s.leaseDeadline = time.Time{}
	s.bindingMu.Unlock()
	if authTimer != nil {
		authTimer.Stop()
	}
	return binding
}

func (s *session) detachForKick(expected locate.GateBinding) bool {
	s.bindingMu.Lock()
	if s.binding != expected || !time.Now().Before(s.leaseDeadline) {
		s.bindingMu.Unlock()
		return false
	}
	s.binding = locate.GateBinding{}
	s.leaseDeadline = time.Time{}
	s.bindingMu.Unlock()
	return true
}

func (s *session) sendIfCurrent(expected locate.GateBinding, msg *v1.Proto) (bool, error) {
	s.bindingMu.Lock()
	defer s.bindingMu.Unlock()
	if s.binding != expected || !time.Now().Before(s.leaseDeadline) {
		return false, nil
	}
	return true, s.conn.SendProto(msg)
}

func (s *session) sendIfAuthenticated(now time.Time, msg *network.PreparedProto) (bool, error) {
	s.bindingMu.Lock()
	defer s.bindingMu.Unlock()
	if !s.leaseValidLocked(now) {
		return false, nil
	}
	if conn, ok := s.conn.(network.PreparedConnection); ok {
		return true, conn.SendPrepared(msg)
	}
	return true, s.conn.SendProto(msg.Message())
}
