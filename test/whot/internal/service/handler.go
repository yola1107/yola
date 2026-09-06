package service

import (
	"context"
	"errors"

	"yola/node"
	"yola/test/internal/mailbox"
	"yola/test/whot/api/v1"
	"yola/test/whot/internal/biz"

	kerrors "github.com/go-kratos/kratos/v3/errors"
)

func (s *Service) OnLoginReq(ctx context.Context, req *v1.LoginReq) (*v1.LoginRsp, error) {
	sess, uid, err := authenticatedSession(ctx)
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, kerrors.BadRequest("INVALID_REQUEST", "login request is missing")
	}
	if req.UserID != uid {
		return nil, kerrors.Unauthorized("UID_MISMATCH", "authenticated UID mismatch")
	}
	reply, err := s.uc.Login(ctx, sess, uid, req.Token)
	return reply, mapBusinessError(err)
}

func (s *Service) OnLogoutReq(ctx context.Context, req *v1.LogoutReq) (*v1.LogoutRsp, error) {
	if req == nil {
		return nil, kerrors.BadRequest("INVALID_REQUEST", "logout request is missing")
	}
	uid, err := requestUID(ctx, req.UserDBID)
	if err != nil {
		return nil, err
	}
	reply, err := s.uc.Logout(ctx, uid)
	return reply, mapBusinessError(err)
}

func (s *Service) OnReadyReq(ctx context.Context, req *v1.ReadyReq) (*v1.ReadyRsp, error) {
	if req == nil {
		return nil, kerrors.BadRequest("INVALID_REQUEST", "ready request is missing")
	}
	uid, err := requestUID(ctx, req.UserID)
	if err != nil {
		return nil, err
	}
	reply, err := s.uc.Ready(ctx, uid, req.IsReady)
	return reply, mapBusinessError(err)
}

func (s *Service) OnSwitchTableReq(ctx context.Context, req *v1.SwitchTableReq) (*v1.SwitchTableRsp, error) {
	if req == nil {
		return nil, kerrors.BadRequest("INVALID_REQUEST", "switch-table request is missing")
	}
	uid, err := requestUID(ctx, req.UserID)
	if err != nil {
		return nil, err
	}
	reply, err := s.uc.SwitchTable(ctx, uid)
	return reply, mapBusinessError(err)
}

func (s *Service) OnSceneReq(ctx context.Context, req *v1.SceneReq) (*v1.SceneRsp, error) {
	if req == nil {
		return nil, kerrors.BadRequest("INVALID_REQUEST", "scene request is missing")
	}
	uid, err := requestUID(ctx, req.UserID)
	if err != nil {
		return nil, err
	}
	reply, err := s.uc.Scene(ctx, uid)
	return reply, mapBusinessError(err)
}

func (s *Service) OnChatReq(ctx context.Context, req *v1.ChatReq) (*v1.ChatRsp, error) {
	if req == nil {
		return nil, kerrors.BadRequest("INVALID_REQUEST", "chat request is missing")
	}
	uid, err := requestUID(ctx, int64(req.UserID))
	if err != nil {
		return nil, err
	}
	reply, err := s.uc.Chat(ctx, uid, req)
	return reply, mapBusinessError(err)
}

func (s *Service) OnHostingReq(ctx context.Context, req *v1.HostingReq) (*v1.HostingRsp, error) {
	if req == nil {
		return nil, kerrors.BadRequest("INVALID_REQUEST", "hosting request is missing")
	}
	uid, err := authenticatedUID(ctx)
	if err != nil {
		return nil, err
	}
	reply, err := s.uc.Hosting(ctx, uid, req.IsHosting)
	return reply, mapBusinessError(err)
}

func (s *Service) OnForwardReq(ctx context.Context, req *v1.ForwardReq) (*v1.ForwardRsp, error) {
	if req == nil {
		return nil, kerrors.BadRequest("INVALID_REQUEST", "forward request is missing")
	}
	uid, err := authenticatedUID(ctx)
	if err != nil {
		return nil, err
	}
	reply, err := s.uc.Forward(ctx, uid, req)
	return reply, mapBusinessError(err)
}

func (s *Service) OnPlayerActionReq(ctx context.Context, req *v1.PlayerActionReq) (*v1.PlayerActionRsp, error) {
	if req == nil {
		return nil, kerrors.BadRequest("INVALID_REQUEST", "player-action request is missing")
	}
	uid, err := requestUID(ctx, req.UserId)
	if err != nil {
		return nil, err
	}
	reply, err := s.uc.PlayerAction(ctx, uid, req)
	return reply, mapBusinessError(err)
}

func authenticatedSession(ctx context.Context) (node.Session, int64, error) {
	sess, ok := node.FromContext(ctx)
	if !ok {
		return nil, 0, kerrors.Unauthorized("INVALID_SESSION", "invalid authenticated session")
	}
	uid, err := biz.SessionUID(sess)
	if err != nil {
		return nil, 0, kerrors.Unauthorized("INVALID_SESSION", "invalid authenticated session")
	}
	return sess, uid, nil
}

func authenticatedUID(ctx context.Context) (int64, error) {
	_, uid, err := authenticatedSession(ctx)
	return uid, err
}

func requestUID(ctx context.Context, claimedUID int64) (int64, error) {
	uid, err := authenticatedUID(ctx)
	if err != nil {
		return 0, err
	}
	if claimedUID != 0 && claimedUID != uid {
		return 0, kerrors.Unauthorized("UID_MISMATCH", "authenticated UID mismatch")
	}
	return uid, nil
}

func mapBusinessError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, biz.ErrPlayerNotFound):
		return kerrors.NotFound("PLAYER_NOT_FOUND", "player not found").WithCause(err)
	case errors.Is(err, biz.ErrInvalidPlayerState):
		return kerrors.BadRequest("INVALID_PLAYER_STATE", "player is not available").WithCause(err)
	case errors.Is(err, biz.ErrReadyRejected):
		return kerrors.BadRequest("READY_REJECTED", "ready request rejected").WithCause(err)
	case errors.Is(err, biz.ErrChatRejected):
		return kerrors.BadRequest("CHAT_REJECTED", "chat request rejected").WithCause(err)
	case errors.Is(err, biz.ErrHostingRejected):
		return kerrors.BadRequest("HOSTING_REJECTED", "hosting request rejected").WithCause(err)
	default:
		return mailbox.MapError(err)
	}
}
