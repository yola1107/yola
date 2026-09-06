package gateway

import (
	"context"
	"errors"
	"log/slog"

	clusterv1 "yola/api/cluster/v1"
	protocolv1 "yola/api/protocol/v1"
	"yola/internal/clusterroute"
	"yola/locate"
	"yola/network"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

type pushService struct {
	clusterv1.UnimplementedGatewayServer
	server *Server
}

var _ clusterv1.GatewayServer = (*pushService)(nil)

func (s *pushService) Push(_ context.Context, in *clusterv1.PushRequest) (*emptypb.Empty, error) {
	if s.server == nil || in == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid push request")
	}
	err := s.server.push(clusterroute.ToBinding(in.GetRoute()), &protocolv1.Proto{
		Op: protocolv1.OpPush, Cmd: in.GetCommand(), Body: in.GetBody(),
	})
	if err != nil {
		return nil, rpcError(err)
	}
	return &emptypb.Empty{}, nil
}

func (s *pushService) Kick(ctx context.Context, in *clusterv1.KickRequest) (*emptypb.Empty, error) {
	if s.server == nil || in == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid kick request")
	}
	err := s.server.kick(ctx, clusterroute.ToBinding(in.GetRoute()), in.GetCode())
	if err != nil {
		return nil, rpcError(err)
	}
	return &emptypb.Empty{}, nil
}

func rpcError(err error) error {
	switch {
	case errors.Is(err, errInvalidPush), errors.Is(err, errInvalidKick):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, errConnectionMissing):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, errBindingChanged):
		return status.Error(codes.Aborted, err.Error())
	case errors.Is(err, errConnectionBusy), errors.Is(err, network.ErrFrameTooLarge):
		return status.Error(codes.ResourceExhausted, err.Error())
	case errors.Is(err, errConnectionClosed):
		return status.Error(codes.Unavailable, err.Error())
	default:
		return status.Error(codes.Internal, "gateway RPC failed")
	}
}

func (s *Server) ownsBinding(binding locate.GateBinding) bool {
	return binding.GateID == s.identity.id && binding.GateEndpoint == s.identity.endpoint
}

// push delivers one message only if target still owns the connection.
func (s *Server) push(binding locate.GateBinding, msg *protocolv1.Proto) error {
	if !locate.ValidGateBinding(binding) || msg == nil || msg.Op != protocolv1.OpPush ||
		!s.ownsBinding(binding) {
		return errInvalidPush
	}
	if !validExternalFrame(msg) {
		return network.ErrFrameTooLarge
	}
	sess := s.sessions.get(binding.ConnID)
	if sess == nil {
		return errConnectionMissing
	}
	matched, err := sess.sendIfCurrent(binding, msg)
	if !matched {
		return errBindingChanged
	}
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, network.ErrFrameTooLarge):
		return network.ErrFrameTooLarge
	case errors.Is(err, network.ErrSendQueueFull):
		return errConnectionBusy
	default:
		return errConnectionClosed
	}
}

func validExternalFrame(msg *protocolv1.Proto) bool {
	return msg != nil && proto.Size(msg) <= protocolv1.MaxProtoSize
}

// kick closes the connection only if target still owns it.
// Session replace intentionally skips notifyDisconnect: the player reconnects and
// Node binding remains until business code calls UnbindNode.
func (s *Server) kick(ctx context.Context, binding locate.GateBinding, code int32) error {
	if !locate.ValidGateBinding(binding) || !s.ownsBinding(binding) || code == 0 {
		return errInvalidKick
	}
	sess := s.sessions.get(binding.ConnID)
	if sess == nil || !sess.detachForKick(binding) {
		return nil
	}
	ctx, cancel := s.cleanupContext(ctx)
	defer cancel()
	// Another path may already have claimed the connection; the close below stays best-effort.
	s.sessions.remove(sess.conn)
	err := sess.conn.CloseWithProto(ctx, &protocolv1.Proto{Op: protocolv1.OpKick, Code: code})
	if err != nil {
		err = errors.Join(err, sess.conn.Close())
	}
	s.unbindGate(ctx, binding)
	if err != nil {
		slog.WarnContext(ctx, "session kick failed",
			"uid", binding.UID,
			"conn_id", binding.ConnID,
			"service", binding.ServiceName,
			"gate_id", binding.GateID,
			"kick_code", code,
			"error", err,
		)
		return nil
	}
	slog.DebugContext(ctx, "session kicked",
		"uid", binding.UID,
		"conn_id", binding.ConnID,
		"service", binding.ServiceName,
		"gate_id", binding.GateID,
		"kick_code", code,
	)
	return nil
}
