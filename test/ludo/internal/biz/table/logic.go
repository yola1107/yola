package table

import (
	"fmt"
	"log/slog"
	"time"

	"yola/test/internal/xgo"
	"yola/test/ludo/api/v1"
	"yola/test/ludo/internal/biz/player"
	"yola/test/ludo/internal/conf"
	"yola/test/ludo/internal/model"
)

const minStartPlayers = 2

func (t *Table) handleStageTimeout() {
	state := t.stage.State()
	if debugLogEnabled() {
		slog.Debug("stage timer expired", "stage", state.String(), "active_chair", t.activeChair, "table", t.Desc())
	}
	switch state {
	case StWait:
		t.tryEnterReady()
	case StReady:
		t.startGame()
	case StSendCard:
		t.onSendCardTimeout()
	case StDice:
		t.onDiceTimeout()
	case StMove:
		t.onMoveTimeout()
	case StResult:
		t.resetForNextGame()
	default:
		slog.Error("unhandled stage timeout", "stage", state.String())
	}
}

func (t *Table) enterStage(state StageID) {
	timeout := time.Duration(state.Timeout()) * time.Second
	t.enterStageFor(state, timeout)
}

func (t *Table) enterStageFor(state StageID, duration time.Duration) {
	t.manager.cancelTimer(t.stage.TimerID())
	generation := t.stage.Set(state, duration, -1)
	timerID := t.manager.after(t.ID, duration, func(gameTable *Table) {
		if gameTable.stage.State() != state || gameTable.stage.Generation() != generation {
			return
		}
		gameTable.handleStageTimeout()
	})
	t.stage.SetTimerID(generation, timerID)
	if timerID < 0 {
		slog.Error("schedule table stage timer", "table_id", t.ID, "stage", state.String())
	}
	if t.mLog.enabled() {
		t.mLog.stage(t.stage.Desc(), t.activeChair)
	}
}

func (t *Table) tryEnterReady() {
	if t.stage.State() != StWait {
		return
	}
	players := t.readyPlayers()
	if !canStartGame(players) {
		return
	}

	if debugLogEnabled() {
		slog.Debug("table has enough ready players",
			"table", t.Desc(),
			"ready_players", len(players),
			"maximum_players", t.maxPlayers,
			"next_stage", StReady.String(),
		)
	}

	t.enterStage(StReady)
}

func (t *Table) startGame() {
	if t.stage.State() != StReady {
		t.enterStage(StWait)
		return
	}

	players := t.readyPlayers()
	if !canStartGame(players) {
		t.enterStage(StWait)
		return
	}

	t.prepareGame(players)
	t.chooseFirstChair(players)
	t.dispatchCardPush(players)
	t.enterStage(StSendCard)

	debugEnabled := debugLogEnabled()
	fileLogEnabled := t.mLog.enabled()
	if !debugEnabled && !fileLogEnabled {
		return
	}
	infos := make([]string, 0, len(players))
	for _, p := range players {
		infos = append(infos, fmt.Sprintf("%d:%d", p.GetPlayerID(), p.GetChairID()))
	}

	if debugEnabled {
		slog.Debug("game started", "table", t.Desc(), "players", infos)
	}
	if fileLogEnabled {
		t.mLog.begin(t.Desc(), t.rc.Game.BaseMoney, t.seats, infos)
	}
}

func (t *Table) readyPlayers() []*player.Player {
	baseMoney := t.rc.Game.BaseMoney
	players := make([]*player.Player, 0, len(t.seats))
	for _, p := range t.seats {
		if p != nil && p.IsReady() && p.GetAllMoney() >= baseMoney {
			players = append(players, p)
		}
	}
	return players
}

func canStartGame(players []*player.Player) bool {
	return len(players) >= minStartPlayers && len(players)%2 == 0
}

func (t *Table) checkAutoReady(p *player.Player) {
	if !t.rc.Game.AutoReady {
		return
	}
	if p != nil && !p.IsReady() && p.GetAllMoney() >= t.rc.Game.BaseMoney {
		p.SetReady()
	}
}

func (t *Table) checkAutoReadyAll() {
	for _, p := range t.seats {
		t.checkAutoReady(p)
	}
}

func (t *Table) prepareGame(seats []*player.Player) {
	baseMoney := t.rc.Game.BaseMoney
	var seatColors []int32
	for _, p := range seats {
		if p == nil {
			continue
		}
		p.SetGaming()
		p.StartGame(baseMoney)
		color := p.GetChairID()
		p.SetColor(color)
		seatColors = append(seatColors, color)
	}

	t.board = model.NewBoard(seatColors, 4, conf.FastMode)

	if conf.FastMode {
		t.gameGeneration++
		generation := t.gameGeneration
		t.fastTimerID = t.manager.after(t.ID, 5*time.Minute, func(gameTable *Table) {
			if gameTable.gameGeneration != generation {
				return
			}
			// 原实现没有快速场胜者规则；超时按流局退还已扣底分。
			gameTable.settleGame(nil, v1.FINISH_TYPE_NONE)
		})
	}

	for _, p := range seats {
		if p == nil || !p.IsGaming() {
			continue
		}
		color := p.GetColor()
		p.SetPieces(t.board.GetPieceIDsByColor(color))
	}
}

func (t *Table) chooseFirstChair(players []*player.Player) {
	index := xgo.RandInt(0, len(players))
	t.activeChair = players[index].GetChairID()
	t.firstChair = t.activeChair
}

func (t *Table) onSendCardTimeout() {
	t.enterStage(StDice)
	t.broadcastActivePlayerPush()
}

func (t *Table) onDiceTimeout() {
	p := t.activePlayer()
	if p == nil {
		slog.Error("dice timeout has no active player", "table", t.Desc())
		return
	}
	if !p.IsGaming() {
		slog.Error("dice timeout player is not gaming", "table", t.Desc(), "player", p.Desc())
		return
	}
	if rsp := t.RollDice(p, true); rsp != nil {
		t.BroadcastDicePush(nil, rsp)
	}
}

func (t *Table) onMoveTimeout() {
	p := t.activePlayer()
	if p == nil {
		slog.Error("move timeout has no active player", "table", t.Desc())
		return
	}
	if !p.IsGaming() {
		slog.Error("move timeout player is not gaming", "table", t.Desc(), "player", p.Desc())
		return
	}

	uid := p.GetPlayerID()
	dice := p.UnusedDice()
	color := p.GetColor()
	chair := p.GetChairID()

	if t.activeChair != chair || t.board == nil || t.stage.State() != StMove {
		return
	}
	id, x := model.FindBestMoveSequence(t.board, dice, color)
	if id <= -1 || x <= -1 {
		slog.Error("move timeout has no movable path", "table", t.Desc(), "player", p.Desc())
		return
	}
	if rsp := t.MovePiece(p, &v1.MoveReq{UserId: uid, PieceId: id, DiceValue: x}, true); rsp != nil {
		t.BroadcastMovePush(nil, rsp)
	}
}

func (t *Table) resetForNextGame() {
	t.reset()
	t.enterStage(StWait)
	t.checkKick()
	t.checkAutoReadyAll()
	if debugLogEnabled() {
		slog.Debug("game cleanup completed", "table", t.Desc())
	}
	if t.mLog.enabled() {
		t.mLog.end(fmt.Sprintf("结束清理完成。%s %s", t.Desc(), logPlayers(t.seats)))
	}
}

func (t *Table) settleGame(winner *player.Player, finishType v1.FINISH_TYPE) *v1.ResultPush {
	t.gameGeneration++
	if t.fastTimerID > 0 {
		t.manager.cancelTimer(t.fastTimerID)
		t.fastTimerID = -1
	}

	baseMoney := t.rc.Game.BaseMoney
	result := &v1.ResultPush{FinishType: finishType}
	if winner == nil {
		for _, p := range t.seats {
			if p == nil || !p.IsGaming() {
				continue
			}
			p.AddMoney(baseMoney)
			result.Results = append(result.Results, playerResult(p, false, 0))
		}
	} else {
		result.WinnerID = winner.GetPlayerID()
		loserCount := 0
		for _, p := range t.seats {
			if p == nil || !p.IsGaming() || p == winner {
				continue
			}
			loserCount++
			result.Results = append(result.Results, playerResult(p, false, -baseMoney))
		}
		payout := baseMoney + float64(loserCount)*baseMoney*(1-t.rc.Game.Fee)
		winner.AddMoney(payout)
		result.Results = append(result.Results, playerResult(winner, true, payout))
	}

	t.enterStage(StResult)
	t.sendToAll(v1.GameCommand_CmdResultPush, result)
	return result
}

func playerResult(p *player.Player, winner bool, score float64) *v1.PlayerResult {
	return &v1.PlayerResult{
		UserID:   p.GetPlayerID(),
		ChairID:  p.GetChairID(),
		IsWinner: winner,
		WinScore: score,
	}
}
