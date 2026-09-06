package table

import (
	"errors"
	"log/slog"

	"yola/test/internal/xgo"
	"yola/test/ludo/api/v1"
	"yola/test/ludo/internal/biz/player"
	"yola/test/ludo/internal/model"
	"yola/test/ludo/pkg/codes"
)

var ErrPlayerExitInProgress = errors.New("player exit is already in progress")

const (
	bonusRollValue  = int32(6)
	maximumDiceSlot = 4
)

func (t *Table) Exit(p *player.Player, code int32, message string) (bool, error) {
	if p == nil {
		return false, nil
	}
	if !p.TryBeginExit() {
		return false, ErrPlayerExitInProgress
	}
	if !t.RemovePlayer(p, false) {
		p.CancelExit()
		return false, nil
	}
	if err := t.repo.LogoutGame(p, code, message); err != nil {
		p.CancelExit()
		return true, err
	}
	return true, nil
}

func (t *Table) Ready(p *player.Player, ready bool) bool {
	if p == nil || t.stage.State() != StWait || t.playerAt(p.GetChairID()) != p {
		return false
	}
	if ready {
		p.SetReady()
		t.tryEnterReady()
	} else {
		p.SetSit()
	}
	return true
}

func (t *Table) SetHosting(p *player.Player, hosting bool) bool {
	if p == nil || t.playerAt(p.GetChairID()) != p {
		return false
	}
	if hosting {
		if p.GetTimeoutCnt() == 0 {
			p.IncrTimeoutCnt(true)
		}
	} else {
		p.ClearTimeoutCnt()
	}
	return true
}

func (t *Table) Offline(p *player.Player) error {
	if p == nil {
		return nil
	}
	t.mLog.offline(p)
	if !p.IsGaming() {
		_, err := t.Exit(p, codes.KickByBroke, "Offline kick by broke")
		return err
	}
	p.SetOffline(true)
	t.broadcastUserOffline(p)
	return nil
}

func (t *Table) RollDice(p *player.Player, timeout bool) *v1.DiceRsp {
	if p == nil || t.stage.State() != StDice || p.GetChairID() != t.activeChair || !p.IsGaming() || p.IsFinish() {
		return nil
	}

	dice := t.ctrlRollDice(p)
	p.AddDice(dice)
	p.IncrTimeoutCnt(timeout)
	rsp := t.diceResponse(p, dice)

	movable := t.hasMovableOption(p)
	tripleSix := dice == bonusRollValue && p.IsTripleSix()

	t.mLog.Dice(p, dice, movable, timeout)
	if debugLogEnabled() {
		slog.Debug("handle dice",
			"player", p.Desc(),
			"dice", dice,
			"movable", movable,
			"triple_six", tripleSix,
			"timeout", timeout,
		)
	}

	// 回合控制
	switch {
	case (!movable) || tripleSix:
		// 无法移动 或 连续三个6，直接结束本轮
		t.endPlayerTurn(p)
	case dice == bonusRollValue:
		// 奖励再掷一次骰子
		t.repeatPlayerTurn(p)
	default:
		// 允许进入移动阶段
		t.allowPlayerToMove(p)
	}
	return rsp
}

func (t *Table) MovePiece(p *player.Player, req *v1.MoveReq, timeout bool) *v1.MoveRsp {
	if p == nil || t.stage.State() != StMove || p.GetChairID() != t.activeChair || !p.IsGaming() || p.IsFinish() {
		return nil
	}
	if !p.HasUnusedDice(req.DiceValue) {
		slog.Error("move rejected: unused dice mismatch",
			"player", p.Desc(),
			"request", req,
			"code", model.ErrDiceMismatch,
		)
		return nil
	}
	if ok, code := t.board.CanMove(p.GetColor(), req.GetPieceId(), req.DiceValue); !ok {
		slog.Error("move rejected: move not allowed", "player", p.Desc(), "request", req, "code", code)
		return nil
	}

	pieceID, dice := req.PieceId, req.DiceValue
	step := t.board.Move(pieceID, dice)
	piece := t.board.GetPieceByID(pieceID)
	arrived := piece.IsArrived()
	p.UseDice(dice)
	p.IncrTimeoutCnt(timeout)
	rsp := t.moveResponse(p, dice, step)

	t.mLog.Move(p, step, arrived, timeout)
	if debugLogEnabled() {
		slog.Debug("handle move",
			"player", p.Desc(),
			"piece_id", pieceID,
			"dice", dice,
			"step", step,
			"timeout", timeout,
		)
	}

	if arrived {
		p.MarkPieceArrived(pieceID)
		if len(t.board.GetActivePieceIDs(p.GetColor())) == 0 {
			p.SetFinish()
		}
		if debugLogEnabled() {
			slog.Debug("piece arrived",
				"player", p.Desc(),
				"piece_id", pieceID,
				"dice", dice,
				"player_finished", p.IsFinish(),
			)
		}

		if p.IsFinish() {
			t.settleGame(p, v1.FINISH_TYPE_PLAYER_HAND_EMPTY)
			return rsp
		}
	}

	switch {
	case arrived || len(step.Killed) > 0:
		// 踩子 或 到达终点, 奖励再掷一次
		t.repeatPlayerTurn(p)
	case !t.hasMovableOption(p):
		// 没有可移动棋子，回合结束
		t.endPlayerTurn(p)
	default:
		// 还有可用骰子继续移动
		t.allowPlayerToMove(p)
	}
	return rsp
}

func (t *Table) hasMovableOption(p *player.Player) bool {
	return t.board.CalcCanMoveDice(p.GetColor(), p.UnusedDice())
}

func (t *Table) allowPlayerToMove(p *player.Player) {
	t.activeChair = p.GetChairID()
	t.enterStage(StMove)
	t.broadcastActivePlayerPush()
}

func (t *Table) repeatPlayerTurn(p *player.Player) {
	t.activeChair = p.GetChairID()
	t.enterStage(StDice)
	t.broadcastActivePlayerPush()
}

func (t *Table) endPlayerTurn(p *player.Player) {
	p.FinishTurn()
	t.activeChair = t.getNextActiveChair()
	t.enterStage(StDice)
	t.broadcastActivePlayerPush()
}

func (t *Table) ctrlRollDice(p *player.Player) int32 {
	// 6点保护策略 (非快速模式。 前 n 次必须至少出过一次6)
	if x := p.RollInitDiceList(); x >= 1 && x <= 6 {
		return x
	}
	// 避免频繁连续6点 66 666
	if p.GetLastRoll() == 6 && xgo.IsHitFloat(0.9) {
		return xgo.RandInt[int32](1, 6)
	}
	// 控制数量
	if len(p.GetDiceSlot()) >= maximumDiceSlot {
		return xgo.RandInt[int32](1, 6)
	}
	return xgo.RandInt[int32](1, 7)
}
