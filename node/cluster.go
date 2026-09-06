package node

import (
	"context"

	"yola/api/cluster/v1"
	"yola/internal/clusterroute"
	"yola/locate"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// DisconnectHandler handles a best-effort client disconnect notification from Gateway.
type DisconnectHandler func(ctx context.Context, sess Session) error

type forwardService struct {
	v1.UnimplementedNodeServer
	server *Server
}

var _ v1.NodeServer = (*forwardService)(nil)

func (s *forwardService) Forward(ctx context.Context, in *v1.ForwardRequest) (*v1.ForwardReply, error) {
	if s.server == nil || in == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid forward request")
	}
	body, err := s.server.forwardTo(
		ctx,
		stickyClaim{NodeID: in.GetNodeId(), Epoch: in.GetNodeEpoch()},
		clusterroute.ToBinding(in.GetRoute()),
		in.GetCommand(),
		in.GetBody(),
	)
	if err != nil {
		return nil, err
	}
	return &v1.ForwardReply{Body: body}, nil
}

func (s *forwardService) Disconnect(ctx context.Context, in *v1.DisconnectRequest) (*emptypb.Empty, error) {
	if s.server == nil || in == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid disconnect request")
	}
	binding := clusterroute.ToBinding(in.GetRoute())
	if !locate.ValidGateBinding(binding) {
		return nil, status.Error(codes.InvalidArgument, "invalid disconnect route")
	}
	if _, err := s.server.routeIdentity(binding); err != nil {
		return nil, err
	}
	if !s.server.requests.admit() {
		return nil, status.Error(codes.Unavailable, "node is draining")
	}
	defer s.server.requests.done()
	ctx, cancel := s.server.lease.Load().requestContext(ctx)
	defer cancel()
	handler := s.server.onDisconnect
	if handler == nil {
		return &emptypb.Empty{}, nil
	}
	sess := requestSession{binding: binding, server: s.server}
	if err := handler(NewContext(ctx, sess), sess); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}
