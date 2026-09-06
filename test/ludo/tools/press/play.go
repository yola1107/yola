package press

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"yola/test/internal/xgo"
	"yola/test/ludo/api/v1"
	"yola/test/ludo/internal/model"
)

const (
	playerStatusSit     = 1
	playerStatusReady   = 2
	playerStatusPlaying = 3
)

func (u *User) login() error {
	rsp := new(v1.LoginRsp)
	err := u.Request(v1.GameCommand_CmdLogin, &v1.LoginReq{UserID: u.id, Token: u.runner.loginToken}, rsp)
	if err != nil {
		return err
	}
	if rsp.Code != 0 {
		return fmt.Errorf("login rejected: code=%d message=%s", rsp.Code, rsp.Msg)
	}
	u.chair.Store(rsp.ChairID)
	u.seated.Store(true)
	return nil
}

func (u *User) scene() (int32, error) {
	scene := new(v1.SceneRsp)
	if err := u.Request(v1.GameCommand_CmdScene, &v1.SceneReq{UserID: u.id}, scene); err != nil {
		return 0, err
	}
	status, found := u.updateScene(scene)
	if !found || status < playerStatusSit {
		return 0, errors.New("scene does not contain a seated pressure user")
	}
	return status, nil
}

func (u *User) markReady(status int32) error {
	if status >= playerStatusReady {
		return nil
	}
	rsp := new(v1.ReadyRsp)
	if err := u.Request(v1.GameCommand_CmdReady, &v1.ReadyReq{UserID: u.id, IsReady: true}, rsp); err != nil {
		return err
	}
	if !rsp.IsReady {
		return errors.New("ready request was not accepted")
	}
	u.ready.Store(true)
	return nil
}

func (u *User) handleActive(active *v1.ActivePush) {
	switch active.CanAction {
	case v1.ACTION_TYPE_AcDice:
		rsp := new(v1.DiceRsp)
		if err := u.Request(v1.GameCommand_CmdDice, &v1.DiceReq{Uid: u.id}, rsp); err != nil {
			u.logActionError("pressure user dice", err)
		} else if rsp.Code != 0 {
			slog.Debug("pressure user dice rejected", "uid", u.id, "code", rsp.Code, "message", rsp.Msg)
		}
	case v1.ACTION_TYPE_AcMove:
		u.sendMove(active.UnusedDices)
	}
}

func (u *User) sendMove(dices []int32) {
	pieceID, dice, found := u.selectMove(dices)
	if !found {
		slog.Debug("pressure user has no legal move", "uid", u.id, "pieces", len(u.pieces), "dices", dices)
		return
	}
	rsp := new(v1.MoveRsp)
	if err := u.Request(v1.GameCommand_CmdMove, &v1.MoveReq{UserId: u.id, PieceId: pieceID, DiceValue: dice}, rsp); err != nil {
		u.logActionError("pressure user move", err)
		return
	}
	if rsp.Code != 0 {
		slog.Debug("pressure user move rejected", "uid", u.id, "piece_id", pieceID, "dice", dice, "code", rsp.Code)
		return
	}
	u.updateMoveResponse(rsp)
}

func (u *User) logActionError(message string, err error) {
	if u.runner.ctx.Err() != nil || errors.Is(err, context.Canceled) {
		slog.Debug(message, "uid", u.id, "error", err)
		return
	}
	slog.Error(message, "uid", u.id, "error", err)
}

func (u *User) selectMove(dices []int32) (int32, int32, bool) {
	for _, dice := range dices {
		for _, piece := range u.pieces {
			if u.colorSet && piece.color == u.color && model.CanMovePiece(piece.pos, piece.color, piece.status, dice) {
				return piece.id, dice, true
			}
		}
	}
	return 0, 0, false
}

func (u *User) updateScene(scene *v1.SceneRsp) (status int32, found bool) {
	for _, player := range scene.Players {
		if player.UserId == u.id {
			u.updatePieces(player.Color, scene.Pieces)
			u.seated.Store(player.Status >= playerStatusSit)
			u.ready.Store(player.Status >= playerStatusReady)
			u.playing.Store(player.Status >= playerStatusPlaying)
			return player.Status, true
		}
	}
	return 0, false
}

func (u *User) updatePieces(color int32, pieces []*v1.Piece) {
	u.color = color
	u.colorSet = true
	u.pieces = u.pieces[:0]
	for _, piece := range pieces {
		if piece != nil && piece.Color == color {
			u.pieces = append(u.pieces, pieceSnapshot{
				id:     piece.Id,
				pos:    piece.Pos,
				color:  piece.Color,
				status: piece.Status,
			})
		}
	}
}

func (u *User) updateMoveResponse(rsp *v1.MoveRsp) {
	if u.colorSet && len(rsp.Pieces) > 0 {
		u.updatePieces(u.color, rsp.Pieces)
	}
}

func (u *User) sendLogoutReq() {
	delay := time.Duration(xgo.RandInt(0, 6000)) * time.Millisecond
	time.AfterFunc(delay, u.logoutAfterDelay)
}

func (u *User) logoutAfterDelay() {
	remove := false
	u.withEvent(func() {
		if err := u.runner.runAction(func() {
			rsp := new(v1.LogoutRsp)
			if err := u.Request(v1.GameCommand_CmdLogout, &v1.LogoutReq{UserDBID: u.id}, rsp); err != nil {
				slog.Error("pressure user logout", "uid", u.id, "error", err)
				return
			}
			if rsp.Code != 0 {
				slog.Error("pressure user logout rejected", "uid", u.id, "code", rsp.Code, "message", rsp.Msg)
				return
			}
			u.chair.Store(-1)
			u.logout.Store(true)
			remove = true
		}); err != nil {
			slog.Debug("skip pressure user logout", "uid", u.id, "error", err)
		}
	})
	if remove {
		u.runner.RemoveUser(u)
	}
}
