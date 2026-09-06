package node

import (
	"context"
	"errors"

	"yola/locate"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// Session is the request-scoped route to the authenticated client.
type Session interface {
	UID() string
	BindingToken() string
	BindNode(context.Context) error
	UnbindNode(context.Context) error
	Push(context.Context, int32, proto.Message) error
}

type sessionKey struct{}

// NewContext returns a context carrying the current Node session.
func NewContext(ctx context.Context, sess Session) context.Context {
	if sess == nil {
		panic("node: nil session")
	}
	return context.WithValue(normalizeContext(ctx), sessionKey{}, sess)
}

// FromContext returns the current Node session, if present.
func FromContext(ctx context.Context) (Session, bool) {
	if ctx == nil {
		return nil, false
	}
	sess, ok := ctx.Value(sessionKey{}).(Session)
	return sess, ok
}

type requestSession struct {
	binding locate.GateBinding
	server  *Server
}

var _ Session = requestSession{}

func (s requestSession) UID() string { return s.binding.UID }

func (s requestSession) BindingToken() string { return s.binding.BindingToken }

func (s requestSession) BindNode(ctx context.Context) error {
	if s.server.locator == nil {
		return status.Error(codes.FailedPrecondition, "node locator is not configured")
	}
	identity, err := s.validatedIdentity()
	if err != nil {
		return err
	}
	return mapNodeLocatorError(s.server.locator.BindNode(
		normalizeContext(ctx), identity.serviceName, s.binding.UID, identity.nodeID,
	))
}

func (s requestSession) UnbindNode(ctx context.Context) error {
	if s.server.locator == nil {
		return status.Error(codes.FailedPrecondition, "node locator is not configured")
	}
	identity, err := s.validatedIdentity()
	if err != nil {
		return err
	}
	return mapNodeLocatorError(s.server.locator.UnbindNode(
		normalizeContext(ctx), identity.serviceName, s.binding.UID, identity.nodeID,
	))
}

func (s requestSession) Push(ctx context.Context, command int32, msg proto.Message) error {
	if !validMessage(msg) {
		return status.Error(codes.InvalidArgument, "push message is required")
	}
	ctx, cancel := context.WithTimeout(normalizeContext(ctx), s.server.pushTimeout)
	defer cancel()
	return s.server.pushToGate(ctx, s.binding, command, msg)
}

func (s requestSession) validatedIdentity() (nodeIdentity, error) {
	identity := s.server.currentIdentity()
	if identity.serviceName != s.binding.ServiceName ||
		!locate.ValidNodeLocation(identity.serviceName, s.binding.UID, identity.nodeID) {
		return nodeIdentity{}, status.Error(codes.FailedPrecondition, "node identity is unavailable")
	}
	return identity, nil
}

func normalizeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func mapNodeLocatorError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.Is(err, locate.ErrInvalidNodeBinding):
		return status.Error(codes.Internal, "invalid node locator state")
	case errors.Is(err, locate.ErrNodeNotFound):
		return status.Error(codes.Aborted, "node binding changed")
	default:
		return status.Error(codes.Unavailable, "node locator is unavailable")
	}
}
