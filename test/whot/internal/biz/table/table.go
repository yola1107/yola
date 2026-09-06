package table

import (
	"fmt"
	"log/slog"
	"sync/atomic"

	"yola/test/whot/api/v1"
	"yola/test/whot/internal/biz/player"
	"yola/test/whot/internal/conf"
	"yola/test/whot/pkg/codes"
)

type Table struct {
	ID         int32
	maxPlayers int32
	rc         *conf.Room
	repo       Repo
	manager    *Manager

	// 游戏变量
	stage *Stage           // 阶段状态
	mLog  *Log             // 桌子日志
	cards *GameCards       // card信息
	seats []*player.Player // 玩家列表

	// 游戏逻辑变量
	seatCount atomic.Int32

	active  int32      // 当前操作玩家
	first   int32      // 第一个操作玩家
	aiLogic RobotLogic // 机器人逻辑

	currCard    int32       // 当前操作的牌 (whot牌指定的花色时,修改currCard的花色为指定花色)
	declareSuit v1.SUIT     // whot牌指定的花色
	pending     *v1.Pending // 当前待处理动作响应; 如等待反击,等待声明花色等操作
}

func newTable(id int32, c *conf.Room, repo Repo, manager *Manager) *Table {
	t := &Table{
		ID:          id,
		maxPlayers:  c.Table.ChairNum,
		rc:          c,
		repo:        repo,
		manager:     manager,
		stage:       &Stage{},
		active:      -1,
		first:       -1,
		currCard:    -1,
		declareSuit: -1,
		pending:     nil,
		cards:       NewGameCards(),
		mLog:        newTableLog(id, c.LogCache),
		seats:       make([]*player.Player, c.Table.ChairNum),
	}
	t.aiLogic.init(t)
	return t
}

func (t *Table) Close() error {
	return t.mLog.Close()
}

func (t *Table) Reset() {
	t.active = -1
	t.first = -1
	t.currCard = -1
	t.declareSuit = -1
	t.pending = nil
	for _, seat := range t.seats {
		if seat == nil {
			continue
		}
		seat.Reset()
	}
}

func (t *Table) Desc() string {
	return fmt.Sprintf("(T:%d SitCnt:%d Gamers:%d St:%+v First:%d CurrCard:[%d] active:%d pending=%v)",
		t.ID, t.seatedCount(), len(t.GetGamers()), t.stage.State(), t.first, t.currCard, t.active, descPending(t.pending))
}

func (t *Table) isFull() bool {
	return t.seatedCount() >= t.maxPlayers
}

func (t *Table) seatedCount() int32 {
	return t.seatCount.Load()
}

func (t *Table) Seat(p *player.Player) bool {
	if p == nil || p.GetTableID() > 0 {
		return false
	}
	for k, v := range t.seats {
		if v != nil {
			continue
		}

		// 桌子信息
		t.seats[k] = p
		seatCount := t.seatCount.Add(1)

		// 玩家信息
		p.Reset()
		p.SetTableID(t.ID)
		p.SetChairID(int32(k))
		p.SetSit()
		t.checkAutoReady(p)

		// 广播入座信息
		t.broadcastUserInfo(p)

		// 发送场景信息
		t.SendSceneInfo(p)

		// 记录进桌时间
		t.aiLogic.markEnterNow()

		// 日志记录
		t.mLog.userEnter(p, seatCount)
		slog.Info("player entered table", "player", p.Desc(), "seat_count", seatCount)

		// 检查是否可开局
		t.checkCanStart()

		return true
	}
	return false
}

func (t *Table) RemovePlayer(p *player.Player, isSwitchTable bool) bool {
	if p == nil {
		return false
	}

	chair := p.GetChairID()
	if p1 := t.playerAt(chair); p1 != p {
		return false
	}

	if !t.canExit(p) {
		return false
	}

	t.seats[p.GetChairID()] = nil
	seatCount := t.seatCount.Add(-1)

	// 广播玩家离桌
	t.broadcastUserQuitPush(p)

	// 记录时间
	t.aiLogic.markExitNow()

	// 重置玩家信息
	nextTableID := player.TableIDDetached
	if isSwitchTable {
		nextTableID = player.TableIDPending
	}
	p.ExitReset(nextTableID)

	t.mLog.userExit(p, seatCount, chair, isSwitchTable)
	slog.Info("player exited table", "player", p.Desc(), "seat_count", seatCount, "stage", t.stage.State(), "switch_table", isSwitchTable)
	return true
}

// ReEnter 重进游戏
func (t *Table) ReEnter(p *player.Player) {
	p.SetOffline(false)

	// 广播入座信息
	t.broadcastUserInfo(p)

	// 发送场景信息
	t.SendSceneInfo(p)

	t.broadcastUserOffline(p)

	seatCount := t.seatedCount()
	t.mLog.userReEnter(p, seatCount)
	slog.Info("player reentered table", "player", p.Desc(), "seat_count", seatCount)
}

func (t *Table) canEnter(p *player.Player) bool {
	return p != nil && !t.isFull()
}

func (t *Table) canExit(p *player.Player) bool {
	stage := t.stage.State()
	return p != nil && !p.IsGaming() && (p.IsOffline() || stage == StWait || stage == StEnd)
}

func (t *Table) canEnterRobot(p *player.Player) bool {
	return t.canEnter(p) && t.aiLogic.CanEnter(p)
}

func (t *Table) canExitRobot(p *player.Player) bool {
	return t.canExit(p) && t.aiLogic.CanExit(p)
}

// LastPlayer 上一家
func (t *Table) LastPlayer(chair int32) *player.Player {
	maxCnt := t.maxPlayers
	for i := int32(0); i < maxCnt; i++ {
		chair--
		if chair < 0 {
			chair = maxCnt - 1
		}
		if t.seats[chair] == nil || !t.seats[chair].IsGaming() {
			continue
		}
		return t.seats[chair]
	}
	return nil
}

// NextPlayer 轮流寻找玩家
func (t *Table) NextPlayer(chair int32) *player.Player {
	maxCnt := t.maxPlayers
	for i := int32(0); i < maxCnt; i++ {
		chair = (chair + 1) % maxCnt
		if t.seats[chair] == nil || !t.seats[chair].IsGaming() {
			continue
		}
		return t.seats[chair]
	}

	return nil
}

func (t *Table) GetActivePlayer() *player.Player {
	active := t.active
	if active < 0 || active >= t.maxPlayers {
		return nil
	}
	return t.seats[active]
}

func (t *Table) GetNextActivePlayer() *player.Player {
	if t.active < 0 || t.active >= t.maxPlayers {
		return nil
	}
	return t.NextPlayer(t.active)
}

func (t *Table) getNextActiveChair() int32 {
	p := t.GetNextActivePlayer()
	if p == nil {
		slog.Error("next active player not found", "active_chair", t.active)
		return -1 // 容错
	}
	return p.GetChairID()
}

func (t *Table) playerAt(chair int32) *player.Player {
	if chair < 0 || chair >= t.maxPlayers {
		return nil
	}
	return t.seats[chair]
}

// GetGamers 返回仍在本局游戏中的玩家。
func (t *Table) GetGamers() []*player.Player {
	var gamers []*player.Player
	for _, p := range t.seats {
		if p == nil || !p.IsGaming() {
			continue
		}
		gamers = append(gamers, p)
	}
	return gamers
}

func (t *Table) Counter() (userCount, robotCount, allCount, gamingCount int32) {
	for _, seat := range t.seats {
		if seat == nil {
			continue
		}
		if seat.IsRobot() {
			robotCount++
		} else {
			userCount++
		}
		if seat.IsGaming() {
			gamingCount++
		}
		allCount++
	}
	return userCount, robotCount, allCount, gamingCount
}

func (t *Table) checkKick() {
	for _, p := range t.seats {
		if p == nil {
			continue
		}
		if code, msg := t.checkKickPlayer(p, t.rc.Game); code != 0 {
			if err := t.OnExitGame(p, code, msg); err != nil {
				slog.Error("kick player", "uid", p.GetPlayerID(), "code", code, "error", err)
			}
		}
	}
}

func (t *Table) checkKickPlayer(p *player.Player, conf *conf.Room_Game) (int32, string) {
	switch t.stage.State() {
	case StWait:
		if p.IsOffline() {
			return codes.KickByBroke, "KICK_BY_BROKE"
		}
		if code, msg := CheckRoomLimit(p, conf); code != 0 {
			return code, msg
		}
	case StWaitEnd, StEnd:
		if p.IsOffline() {
			return codes.KickByBroke, "KICK_BY_BROKE"
		}
	default:
	}
	return 0, ""
}
