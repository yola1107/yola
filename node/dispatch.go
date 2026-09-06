package node

import (
	"context"
	"fmt"

	"yola/locate"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Handler handles a raw command body. The current Session is available through FromContext.
type Handler func(context.Context, []byte) ([]byte, error)

// stickyClaim carries Gateway sticky-routing identity for Stateful Node requests.
type stickyClaim struct {
	NodeID string
	Epoch  string
}

// RegisterRawHandler is the escape hatch for custom codecs and handler decorators.
// It does not apply node.Middleware and must be called before BeforeStart.
func (s *Server) RegisterRawHandler(command int32, handler Handler) {
	if handler == nil {
		panic("node: nil handler")
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.state != stNew || s.requests.isClosed() {
		panic("node: handlers must be registered before BeforeStart")
	}
	if _, exists := s.handlers[command]; exists {
		panic(fmt.Sprintf("node: duplicate Command=%d", command))
	}
	s.handlers[command] = handler
}

// OnDisconnect registers a best-effort handler before BeforeStart.
// Passing nil clears the handler. At-most-once delivery; may reorder relative to Forward.
func (s *Server) OnDisconnect(handler DisconnectHandler) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.state != stNew || s.requests.isClosed() {
		panic("node: handlers must be registered before BeforeStart")
	}
	s.onDisconnect = handler
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
	if err := s.fenceStickyClaim(ctx, claim, binding, identity); err != nil {
		return nil, err
	}
	handler := s.handlers[command]
	if handler == nil {
		return nil, status.Errorf(codes.Unimplemented, "unimplemented Command=%d", command)
	}
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
	if identity.nodeID == "" || identity.nodeID != claim.NodeID {
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
	return identity, nil
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
	return nil
}
