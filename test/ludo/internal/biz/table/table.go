package table

import (
	"fmt"
	"log/slog"
	"sync/atomic"

	"yola/test/ludo/internal/biz/player"
	"yola/test/ludo/internal/conf"
	"yola/test/ludo/internal/model"
	"yola/test/ludo/pkg/codes"
)

type Table struct {
	ID        int32
	rc        *conf.Room
	repo      Repo
	manager   *Manager
	stage     Stage
	mLog      *Log
	seats     []*player.Player
	seatCount atomic.Int32

	activeChair    int32
	firstChair     int32
	robotLogic     RobotLogic
	board          *model.Board
	fastTimerID    int64
	gameGeneration uint64
}

type playerCounts struct {
	users   int32
	robots  int32
	playing int32
}

func newTable(tableID int32, room *conf.Room, repo Repo, tableLog *Log, manager *Manager) *Table {
	t := &Table{
		ID:          tableID,
		rc:          room,
		repo:        repo,
		manager:     manager,
		mLog:        tableLog,
		seats:       make([]*player.Player, room.Table.ChairNum),
		activeChair: -1,
		firstChair:  -1,
		fastTimerID: -1,
	}
	t.robotLogic.table = t
	return t
}

func (t *Table) reset() {
	t.activeChair = -1
	t.firstChair = -1
	t.board.Clear()
	t.board = nil

	for _, seat := range t.seats {
		if seat == nil {
			continue
		}
		seat.Reset()
	}
}

func (t *Table) Desc() string {
	counts := t.countPlayers()
	return fmt.Sprintf("(T:%d SitCnt:%d Gamers:%d St:%+v First:%d active:%d )",
		t.ID, t.seatedCount(), counts.playing, t.stage.State(), t.firstChair, t.activeChair)
}

func (t *Table) isFull() bool {
	return int(t.seatedCount()) >= len(t.seats)
}

func (t *Table) seatedCount() int32 { return t.seatCount.Load() }

func (t *Table) Seat(p *player.Player) (bool, error) {
	if p == nil {
		return false, nil
	}
	for k, v := range t.seats {
		if v != nil {
			continue
		}

		t.seats[k] = p
		seatCount := t.seatCount.Add(1)

		p.Reset()
		p.SetOffline(false)
		p.SetTableID(t.ID)
		p.SetChairID(int32(k))
		p.SetSit()
		t.checkAutoReady(p)

		if err := t.broadcastUserInfo(p); err != nil {
			return false, err
		}
		if err := t.sendScene(p); err != nil {
			return false, err
		}
		t.robotLogic.markEnterNow()
		t.mLog.userEnter(p, seatCount)
		slog.Info("player entered table", "player", p.Desc(), "seat_count", seatCount)
		t.tryEnterReady()
		return true, nil
	}
	return false, nil
}

func (t *Table) RemovePlayer(p *player.Player, isSwitchTable bool) bool {
	if p == nil {
		return false
	}

	chair := p.GetChairID()
	if current := t.playerAt(chair); current != p {
		return false
	}

	if !t.canExit(p) {
		return false
	}

	t.seats[chair] = nil
	seatCount := t.seatCount.Add(-1)

	t.broadcastUserQuitPush(p)
	t.robotLogic.markExitNow()
	nextTableID := player.TableIDDetached
	if isSwitchTable {
		nextTableID = player.TableIDPending
	}
	p.ExitReset(nextTableID)
	t.mLog.userExit(p, seatCount, chair, isSwitchTable)
	slog.Info("player exited table",
		"player", p.Desc(),
		"seat_count", seatCount,
		"stage", t.stage.State().String(),
		"switch_table", isSwitchTable,
	)
	return true
}

func (t *Table) ReEnter(p *player.Player) error {
	if p == nil {
		return nil
	}
	p.SetOffline(false)

	if err := t.broadcastUserInfo(p); err != nil {
		return err
	}
	if err := t.sendScene(p); err != nil {
		return err
	}
	t.broadcastUserOffline(p)

	seatCount := t.seatedCount()
	t.mLog.userReEnter(p, seatCount)
	slog.Info("player re-entered table", "player", p.Desc(), "seat_count", seatCount)
	return nil
}

func (t *Table) canExit(p *player.Player) bool {
	if p == nil {
		return false
	}
	switch t.stage.State() {
	case StWait:
		return !p.IsGaming()
	case StResult:
		return true
	default:
		return false
	}
}

func (t *Table) canRobotExit(p *player.Player) bool {
	return t.canExit(p) && t.robotLogic.canExit(p)
}

func (t *Table) nextPlayer(chair int) *player.Player {
	for range t.seats {
		chair = (chair + 1) % len(t.seats)
		p := t.seats[chair]
		if p == nil || !p.IsGaming() || p.IsFinish() {
			continue
		}
		return p
	}

	return nil
}

func (t *Table) activePlayer() *player.Player {
	return t.playerAt(t.activeChair)
}

func (t *Table) getNextActiveChair() int32 {
	if chair := int(t.activeChair); chair >= 0 && chair < len(t.seats) {
		if p := t.nextPlayer(chair); p != nil {
			return p.GetChairID()
		}
	}
	slog.Error("next active player not found", "active_chair", t.activeChair, "table", t.Desc())
	return -1
}

func (t *Table) playerAt(chair int32) *player.Player {
	if chair < 0 || int(chair) >= len(t.seats) {
		return nil
	}
	return t.seats[chair]
}

func (t *Table) countPlayers() playerCounts {
	var counts playerCounts
	for _, seat := range t.seats {
		if seat == nil {
			continue
		}
		if seat.IsRobot() {
			counts.robots++
		} else {
			counts.users++
		}
		if seat.IsGaming() && !seat.IsFinish() {
			counts.playing++
		}
	}
	return counts
}

func (t *Table) checkKick() {
	for _, p := range t.seats {
		if p == nil {
			continue
		}
		if code, msg := t.checkKickPlayer(p, t.rc.Game); code != codes.Success {
			if _, err := t.Exit(p, code, msg); err != nil {
				slog.Error("kick player", "player", p.Desc(), "error", err)
			}
		}
	}
}

func (t *Table) checkKickPlayer(p *player.Player, game *conf.Room_Game) (int32, string) {
	stage := t.stage.State()
	if (stage == StWait || stage == StResult) && p.IsOffline() {
		return codes.KickByBroke, "KICK_BY_BROKE"
	}
	if stage == StWait {
		return CheckRoomLimit(p, game)
	}
	return codes.Success, ""
}
