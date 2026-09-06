package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	clusterv1 "yola/api/cluster/v1"
	protocolv1 "yola/api/protocol/v1"
	"yola/internal/clusterroute"
	"yola/locate"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Server) forward(ctx context.Context, sess *session, msg *protocolv1.Proto) {
	binding, valid := sess.route(time.Now())
	if !valid {
		_ = sess.conn.Close()
		msg.Op, msg.Code, msg.Body = protocolv1.OpResponse, int32(codes.Unauthenticated), nil
		return
	}
	rpcCtx, cancel := context.WithTimeout(ctx, s.rpcTimeout)
	defer cancel()
	client, nodeID, nodeEpoch, err := s.resolveForwardRoute(rpcCtx, binding)
	if err != nil {
		code := forwardStatusCode(err)
		switch code {
		case codes.Unavailable:
			slog.ErrorContext(ctx, "forward node unavailable",
				"uid", binding.UID,
				"conn_id", binding.ConnID,
				"service", binding.ServiceName,
				"gate_id", binding.GateID,
				"code", int32(code),
				"status", code.String(),
				"error", err,
			)
		case codes.Aborted, codes.Internal:
			slog.WarnContext(ctx, "forward node fenced",
				"uid", binding.UID,
				"conn_id", binding.ConnID,
				"service", binding.ServiceName,
				"gate_id", binding.GateID,
				"code", int32(code),
				"status", code.String(),
				"error", err,
			)
		}
		msg.Op, msg.Code, msg.Body = protocolv1.OpResponse, int32(code), nil
		return
	}
	reply, err := client.Forward(withNodeID(rpcCtx, nodeID), &clusterv1.ForwardRequest{
		Route:     clusterroute.FromBinding(binding),
		Command:   msg.Cmd,
		Body:      msg.Body,
		NodeId:    nodeID,
		NodeEpoch: nodeEpoch,
	}, grpc.WaitForReady(nodeID != ""))
	if err != nil {
		code := status.Code(err)
		if code != codes.Canceled && code != codes.Aborted {
			slog.ErrorContext(ctx, "forward node failed",
				"uid", binding.UID,
				"conn_id", binding.ConnID,
				"service", binding.ServiceName,
				"gate_id", binding.GateID,
				"code", int32(code),
				"status", code.String(),
				"error", err,
			)
		}
		msg.Op, msg.Code, msg.Body = protocolv1.OpResponse, int32(code), nil
		return
	}
	msg.Op, msg.Code, msg.Body = protocolv1.OpResponse, int32(codes.OK), reply.GetBody()
	if !validExternalFrame(msg) {
		msg.Code, msg.Body = int32(codes.ResourceExhausted), nil
	}
}

// resolveForwardRoute adds the epoch required to fence a bound sticky Forward.
func (s *Server) resolveForwardRoute(ctx context.Context, binding locate.GateBinding) (clusterv1.NodeClient, string, string, error) {
	client, nodeID, err := s.resolveNodeRoute(ctx, binding)
	if err != nil || nodeID == "" {
		return client, nodeID, "", err
	}
	epoch, err := s.locator.LocateNodeEpoch(ctx, binding.ServiceName, nodeID)
	if err != nil {
		return nil, "", "", err
	}
	return client, nodeID, epoch, nil
}

// resolveNodeRoute selects the service backend and any bound sticky NodeID.
func (s *Server) resolveNodeRoute(ctx context.Context, binding locate.GateBinding) (clusterv1.NodeClient, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	nodeBackend, err := s.backends.get(ctx, binding.ServiceName)
	if err != nil {
		return nil, "", err
	}
	snapshot := nodeBackend.state.load()
	if !snapshot.available {
		return nil, "", fmt.Errorf("gateway: node service %q is unavailable", binding.ServiceName)
	}
	if !snapshot.sticky {
		return nodeBackend.client, "", nil
	}
	nodeID, err := s.locator.LocateNode(ctx, binding.ServiceName, binding.UID)
	if errors.Is(err, locate.ErrNodeNotFound) {
		return nodeBackend.client, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	if !locate.ValidNodeLocation(binding.ServiceName, binding.UID, nodeID) {
		return nil, "", locate.ErrInvalidNodeBinding
	}
	if _, exists := snapshot.hostByNodeID[nodeID]; !exists {
		return nil, "", fmt.Errorf("gateway: node %q is unavailable", nodeID)
	}
	return nodeBackend.client, nodeID, nil
}

func forwardStatusCode(err error) codes.Code {
	switch {
	case errors.Is(err, context.Canceled):
		return codes.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return codes.DeadlineExceeded
	case errors.Is(err, locate.ErrInvalidNodeBinding), errors.Is(err, locate.ErrInvalidNodeEpoch):
		return codes.Internal
	case errors.Is(err, locate.ErrNodeEpochNotFound), errors.Is(err, locate.ErrNodeEpochConflict):
		return codes.Aborted
	default:
		return codes.Unavailable
	}
}

func (s *Server) notifyDisconnect(ctx context.Context, binding locate.GateBinding) {
	client, nodeID, err := s.resolveNodeRoute(ctx, binding)
	if err != nil {
		slog.WarnContext(ctx, "disconnect notify unavailable",
			"uid", binding.UID,
			"conn_id", binding.ConnID,
			"service", binding.ServiceName,
			"gate_id", binding.GateID,
			"error", err,
		)
		return
	}
	_, err = client.Disconnect(withNodeID(ctx, nodeID), &clusterv1.DisconnectRequest{
		Route: clusterroute.FromBinding(binding),
	}, grpc.WaitForReady(nodeID != ""))
	if err != nil {
		slog.WarnContext(ctx, "disconnect notify failed",
			"uid", binding.UID,
			"conn_id", binding.ConnID,
			"service", binding.ServiceName,
			"gate_id", binding.GateID,
			"node_id", nodeID,
			"error", err,
		)
	}
}
