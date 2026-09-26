package node

import (
	"context"

	"yola/locate"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// stickyClaim carries Gateway sticky-routing identity for Stateful Node requests.
type stickyClaim struct {
	NodeID string
	Epoch  string
}

// forwardTo validates the target service and any Stateful claim before dispatching the command.
func (s *Server) forwardTo(ctx context.Context, claim stickyClaim, binding locate.GateBinding, command int32, body []byte) ([]byte, error) {
	if !s.requests.admit() {
		return nil, status.Error(codes.Unavailable, "node is draining")
	}
	defer s.requests.done()
	if !locate.ValidGateBinding(binding) {
		return nil, status.Error(codes.InvalidArgument, "invalid forward route")
	}
	identity, err := s.routeIdentity(binding)
	if err != nil {
		return nil, err
	}
	ctx, cancel := s.lease.Load().requestContext(ctx)
	defer cancel()
	if err := s.fenceStickyClaim(ctx, claim, binding, identity); err != nil {
		return nil, err
	}
	handler := s.handlers[command]
	if handler == nil {
		return nil, status.Errorf(codes.Unimplemented, "unimplemented Command=%d", command)
	}
	ctx = context.WithValue(ctx, commandKey{}, command)
	return handler(NewContext(ctx, requestSession{binding: binding, server: s}), body)
}

// fenceStickyClaim requires paired NodeID/epoch fields and applies sticky-route fencing.
func (s *Server) fenceStickyClaim(ctx context.Context, claim stickyClaim, binding locate.GateBinding, identity nodeIdentity) error {
	if (claim.NodeID == "") != (claim.Epoch == "") {
		return status.Error(codes.InvalidArgument, "invalid node claim")
	}
	if claim.NodeID == "" {
		return nil
	}
	if identity.nodeID != claim.NodeID {
		return status.Error(codes.Aborted, "node binding changed")
	}
	if claim.Epoch != identity.epoch {
		return status.Error(codes.Aborted, "node epoch mismatch")
	}
	return s.checkNodeBinding(ctx, binding, identity)
}

func (s *Server) routeIdentity(binding locate.GateBinding) (nodeIdentity, error) {
	identity := s.currentIdentity()
	if identity.serviceName == "" || identity.serviceName != binding.ServiceName {
		return nodeIdentity{}, status.Error(codes.Aborted, "node service mismatch")
	}
	return identity, s.checkEpoch()
}

func (s *Server) checkEpoch() error {
	if err := s.lease.Load().valid(); err != nil {
		s.failLifecycle(err)
		return status.Error(codes.Unavailable, "node epoch is unavailable")
	}
	return nil
}

func (s *Server) checkNodeBinding(ctx context.Context, gate locate.GateBinding, identity nodeIdentity) error {
	if s.locator == nil {
		return status.Error(codes.Unavailable, "node locator is unavailable")
	}
	nodeID, err := s.locator.LocateNode(ctx, gate.ServiceName, gate.UID)
	if err != nil {
		return mapNodeLocatorError(err)
	}
	if nodeID != identity.nodeID {
		return status.Error(codes.Aborted, "node binding changed")
	}
	// 定位 I/O 可能跨过租约截止时间，迟到的成功结果不得放行 handler。
	return s.checkEpoch()
}
