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

// DisconnectHandler 处理 Gateway 尽力发送的客户端断线通知。
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

// stickyClaim 携带 Gateway 为 Stateful 请求声明的目标身份。
type stickyClaim struct {
	NodeID string
	Epoch  string
}

// forwardTo 核验目标服务和 Stateful 路由声明后分发 command。
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

// fenceStickyClaim 要求 NodeID 与 epoch 成对出现，并核验粘性路由归属。
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
