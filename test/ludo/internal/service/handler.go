package service

import (
	"context"
	"errors"
	"strconv"

	"yola/node"
	"yola/test/internal/mailbox"
	"yola/test/ludo/api/v1"
	"yola/test/ludo/internal/biz"

	kerrors "github.com/go-kratos/kratos/v3/errors"
)

func (s *Service) OnLoginReq(ctx context.Context, req *v1.LoginReq) (*v1.LoginRsp, error) {
	sess, uid, err := requestSession(ctx, req.UserID)
	if err != nil {
		return nil, err
	}
	reply, err := s.usecase.Login(ctx, sess, uid, req.Token)
	return reply, mapBusinessError(err)
}

func (s *Service) OnLogoutReq(ctx context.Context, req *v1.LogoutReq) (*v1.LogoutRsp, error) {
	uid, err := requestUID(ctx, req.UserDBID)
	if err != nil {
		return nil, err
	}
	reply, err := s.usecase.Logout(ctx, uid)
	return reply, mapBusinessError(err)
}

func (s *Service) OnReadyReq(ctx context.Context, req *v1.ReadyReq) (*v1.ReadyRsp, error) {
	uid, err := requestUID(ctx, req.UserID)
	if err != nil {
		return nil, err
	}
	reply, err := s.usecase.Ready(ctx, uid, req.IsReady)
	return reply, mapBusinessError(err)
}

func (s *Service) OnSwitchTableReq(ctx context.Context, req *v1.SwitchTableReq) (*v1.SwitchTableRsp, error) {
	uid, err := requestUID(ctx, req.UserID)
	if err != nil {
		return nil, err
	}
	reply, err := s.usecase.SwitchTable(ctx, uid)
	return reply, mapBusinessError(err)
}

func (s *Service) OnSceneReq(ctx context.Context, req *v1.SceneReq) (*v1.SceneRsp, error) {
	uid, err := requestUID(ctx, req.UserID)
	if err != nil {
		return nil, err
	}
	reply, err := s.usecase.Scene(ctx, uid)
	return reply, mapBusinessError(err)
}

func (s *Service) OnChatReq(ctx context.Context, req *v1.ChatReq) (*v1.ChatRsp, error) {
	uid, err := requestUID(ctx, int64(req.UserID))
	if err != nil {
		return nil, err
	}
	reply, err := s.usecase.Chat(ctx, uid, req)
	return reply, mapBusinessError(err)
}

func (s *Service) OnHostingReq(ctx context.Context, req *v1.HostingReq) (*v1.HostingRsp, error) {
	uid, err := authenticatedUID(ctx)
	if err != nil {
		return nil, err
	}
	reply, err := s.usecase.Hosting(ctx, uid, req.IsHosting)
	return reply, mapBusinessError(err)
}

func (s *Service) OnForwardReq(ctx context.Context, req *v1.ForwardReq) (*v1.ForwardRsp, error) {
	uid, err := authenticatedUID(ctx)
	if err != nil {
		return nil, err
	}
	reply, err := s.usecase.Forward(ctx, uid, req)
	return reply, mapBusinessError(err)
}

func (s *Service) OnDiceReq(ctx context.Context, req *v1.DiceReq) (*v1.DiceRsp, error) {
	uid, err := requestUID(ctx, req.Uid)
	if err != nil {
		return nil, err
	}
	reply, err := s.usecase.Dice(ctx, uid)
	return reply, mapBusinessError(err)
}

func (s *Service) OnMoveReq(ctx context.Context, req *v1.MoveReq) (*v1.MoveRsp, error) {
	uid, err := requestUID(ctx, req.UserId)
	if err != nil {
		return nil, err
	}
	reply, err := s.usecase.Move(ctx, uid, req)
	return reply, mapBusinessError(err)
}

func authenticatedSession(ctx context.Context) (node.Session, int64, error) {
	sess, ok := node.FromContext(ctx)
	if !ok {
		return nil, 0, kerrors.Unauthorized("SESSION_NOT_FOUND", "authenticated session is missing")
	}
	rawUID := sess.UID()
	uid, err := strconv.ParseInt(rawUID, 10, 64)
	if err != nil || uid <= 0 || strconv.FormatInt(uid, 10) != rawUID {
		return nil, 0, kerrors.Unauthorized("INVALID_UID", "authenticated UID must be a positive canonical integer")
	}
	return sess, uid, nil
}

func authenticatedUID(ctx context.Context) (int64, error) {
	_, uid, err := authenticatedSession(ctx)
	return uid, err
}

func requestUID(ctx context.Context, claimedUID int64) (int64, error) {
	_, uid, err := requestSession(ctx, claimedUID)
	return uid, err
}

func requestSession(ctx context.Context, claimedUID int64) (node.Session, int64, error) {
	sess, uid, err := authenticatedSession(ctx)
	if err != nil {
		return nil, 0, err
	}
	if uid != claimedUID {
		return nil, 0, kerrors.Unauthorized("UID_MISMATCH", "request user ID does not match authenticated UID")
	}
	return sess, uid, nil
}

func mapBusinessError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, biz.ErrInvalidPlayerState):
		return kerrors.BadRequest("INVALID_PLAYER_STATE", "player is not available").WithCause(err)
	case errors.Is(err, biz.ErrReadyRejected):
		return kerrors.BadRequest("READY_REJECTED", "ready request rejected").WithCause(err)
	case errors.Is(err, biz.ErrHostingRejected):
		return kerrors.BadRequest("HOSTING_REJECTED", "hosting request rejected").WithCause(err)
	default:
		return mailbox.MapError(err)
	}
}
