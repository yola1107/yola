package table

import (
	"log/slog"
	"time"

	"yola/test/internal/xgo"
	"yola/test/ludo/api/v1"
	"yola/test/ludo/internal/biz/player"
	"yola/test/ludo/internal/model"

	"google.golang.org/protobuf/proto"
)

const (
	robotEnterMinIntervalSeconds = 1
	robotEnterMaxIntervalSeconds = 5
	robotExitMinIntervalSeconds  = 1
	robotExitMaxIntervalSeconds  = 5
	robotExitChance              = 0.05
)

// RobotLogic 封装机器人在桌上的行为逻辑
type RobotLogic struct {
	table         *Table
	lastEnterUnix int64
	lastExitUnix  int64
}

func (r *RobotLogic) markEnterNow() {
	r.lastEnterUnix = time.Now().Unix()
}

func (r *RobotLogic) markExitNow() {
	r.lastExitUnix = time.Now().Unix()
}

func (r *RobotLogic) enteredRecently() bool {
	elapsedSec := time.Now().Unix() - r.lastEnterUnix
	return elapsedSec < int64(xgo.RandIntInclusive(robotEnterMinIntervalSeconds, robotEnterMaxIntervalSeconds))
}

func (r *RobotLogic) exitedRecently() bool {
	elapsedSec := time.Now().Unix() - r.lastExitUnix
	return elapsedSec < int64(xgo.RandIntInclusive(robotExitMinIntervalSeconds, robotExitMaxIntervalSeconds))
}

func (r *RobotLogic) canEnter(p *player.Player) bool {
	if p == nil || r.table == nil {
		return false
	}
	cfg := r.table.rc.Robot
	if !cfg.Open {
		return false
	}
	if r.table.isFull() || r.enteredRecently() {
		return false
	}

	// 前 N 桌允许纯 Robot，避免没有真人时 Robot 永远无法入桌。
	reservedTables := int32(0)
	if cfg.TableMaxCount > 0 && cfg.MinPlayCount > 0 {
		reservedTables = cfg.MinPlayCount / cfg.TableMaxCount
		if cfg.MinPlayCount%cfg.TableMaxCount != 0 {
			reservedTables++
		}
	}

	counts := r.table.countPlayers()
	switch {
	case counts.robots >= cfg.TableMaxCount:
		return false
	case reservedTables > 0 && r.table.ID <= reservedTables:
		return true
	case counts.users == 0:
		return false
	default:
		return true
	}
}

func (r *RobotLogic) canExit(p *player.Player) bool {
	if p == nil || r.table == nil {
		return false
	}
	cfg := r.table.rc.Robot
	if !cfg.Open {
		return true
	}
	if r.exitedRecently() {
		return false
	}
	counts := r.table.countPlayers()
	money := p.GetAllMoney()
	switch {
	case counts.users == 0, counts.robots > cfg.TableMaxCount:
		return true
	case money >= cfg.StandMaxMoney, money <= cfg.StandMinMoney:
		return true
	default:
		return xgo.IsHitFloat(robotExitChance)
	}
}

func (r *RobotLogic) onMessage(p *player.Player, cmd v1.GameCommand, msg proto.Message) {
	if p == nil {
		return
	}
	switch cmd {
	case v1.GameCommand_CmdActivePush:
		r.onActivePlayer(p, msg)
	case v1.GameCommand_CmdResultPush:
		r.onExit(p, msg)
	default:
	}
}

func (r *RobotLogic) onExit(p *player.Player, _ proto.Message) {
	if !r.table.canRobotExit(p) {
		return
	}
	delay := time.Duration(xgo.RandInt(robotExitMinIntervalSeconds, robotExitMaxIntervalSeconds)) * time.Second
	uid := p.GetPlayerID()
	chair := p.GetChairID()
	r.table.manager.after(r.table.ID, delay, func(gameTable *Table) {
		current := gameTable.playerAt(chair)
		if current == nil || current.GetPlayerID() != uid || !gameTable.canRobotExit(current) {
			return
		}
		_, _ = gameTable.Exit(current, 0, "ai exit")
	})
}

func (r *RobotLogic) onActivePlayer(p *player.Player, msg proto.Message) {
	rsp, ok := msg.(*v1.ActivePush)
	if !ok || rsp == nil || !p.IsGaming() || p.IsFinish() {
		return
	}
	if p.GetChairID() != rsp.Active || p.GetChairID() != r.table.activeChair {
		return
	}

	stage := r.table.stage.State()
	if stage != StMove && stage != StDice {
		return
	}

	if stage == StDice {
		chair := p.GetChairID()
		uid := p.GetPlayerID()
		stageGeneration := r.table.stage.Generation()

		delay := time.Duration(xgo.RandInt(1000, 2200)) * time.Millisecond
		r.table.manager.after(r.table.ID, delay, func(gameTable *Table) {
			current := gameTable.playerAt(chair)
			if current == nil || current.GetPlayerID() != uid || !current.IsGaming() || current.IsFinish() {
				return
			}
			if gameTable.activeChair != chair || gameTable.stage.State() != StDice || gameTable.stage.Generation() != stageGeneration {
				return
			}
			if rsp := gameTable.RollDice(current, false); rsp != nil {
				gameTable.BroadcastDicePush(nil, rsp)
			}
		})
		return
	}

	// 移动阶段：快照闭包需要的玩家状态。
	uid := p.GetPlayerID()
	chair := p.GetChairID()
	color := p.GetColor()
	stageGeneration := r.table.stage.Generation()
	dices := append([]int32(nil), p.UnusedDice()...)
	delay := time.Duration(xgo.RandInt(1000, int(r.table.stage.Remaining().Milliseconds()*3/4))) * time.Millisecond

	bestID, bestDice := model.FindBestMoveSequence(r.table.board, dices, color)
	if bestID <= -1 || bestDice <= -1 {
		slog.Error("robot has no movable path",
			"table", r.table.Desc(),
			"player", p.Desc(),
			"dice", dices,
			"piece_id", bestID,
			"move", xgo.ToJSON(rsp),
		)
		return
	}

	r.table.manager.after(r.table.ID, delay, func(gameTable *Table) {
		current := gameTable.playerAt(chair)
		if current == nil || current.GetPlayerID() != uid || !current.IsGaming() || current.IsFinish() {
			return
		}
		if gameTable.activeChair != chair || gameTable.stage.State() != StMove || gameTable.stage.Generation() != stageGeneration {
			return
		}
		rsp := gameTable.MovePiece(current, &v1.MoveReq{
			UserId:    uid,
			PieceId:   bestID,
			DiceValue: bestDice,
		}, false)
		if rsp != nil {
			gameTable.BroadcastMovePush(nil, rsp)
		}
	})
}
