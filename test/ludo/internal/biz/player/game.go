package player

import (
	"fmt"
	"sync/atomic"

	"yola/test/internal/xgo"
	"yola/test/ludo/internal/conf"
)

// 非正 Table ID 区分已被 Manager 接管但尚未入桌与已经离桌的玩家。
const (
	TableIDPending  int32 = 0
	TableIDDetached int32 = -1
)

const (
	maximumDiceValue = int32(6)
	tripleRollLimit  = 3
)

// 玩家状态枚举
const (
	StFree     Status = iota // 空闲
	StSit                    // 入座
	StReady                  // 准备
	StGaming                 // 游戏中
	StGameFold               // 弃权（暂未使用）
	StGameLost               // 失败（暂未使用）
)

// Status 表示玩家当前的状态
type Status int32

// String 返回状态的字符串表示
func (s Status) String() string {
	switch s {
	case StFree:
		return "Free"
	case StSit:
		return "Sit"
	case StReady:
		return "Ready"
	case StGaming:
		return "Gaming"
	case StGameFold:
		return "Fold"
	case StGameLost:
		return "Lost"
	default:
		return fmt.Sprintf("%d", s)
	}
}

// DiceSlot 表示一个骰子及其是否已被使用
type DiceSlot struct {
	Value int32 // 骰子点数
	Used  bool  // 是否已使用
}

// GameData 存储玩家在对局中的动态信息
type GameData struct {
	TableID   atomic.Int32 // 所在桌号
	ChairID   atomic.Int32 // 座位号
	status    Status       // 玩家状态
	idleCount int32        // 超时次数
	isOffline atomic.Bool  // 是否离线

	color        int32      // 颜色：0红 1黄 2绿 3蓝
	initDiceList []int32    // 6点保护策略
	lastRoll     int32      // 最近摇骰子点数，供 Robot 决策使用
	dices        []DiceSlot // 当前轮骰子
	lastDices    []DiceSlot // 上轮骰子
	allArrived   bool       // 是否所有棋子进入终点
	pieceIDs     []int32    // 当前持有的棋子ID列表
}

// Reset 清除玩家的游戏状态（不清除座位与桌号）
func (p *Player) Reset() {
	p.gameData.status = StFree
	p.gameData.idleCount = 0
	p.gameData.color = -1
	p.gameData.initDiceList = nil
	p.gameData.lastRoll = -1
	p.gameData.dices = nil
	p.gameData.lastDices = nil
	p.gameData.allArrived = false
	p.gameData.pieceIDs = nil
}

// ExitReset 清除离桌状态，并直接发布下一路由状态，避免 Switch 暴露短暂游离状态。
func (p *Player) ExitReset(nextTableID int32) {
	p.Reset()
	p.gameData.ChairID.Store(-1)
	p.gameData.status = -1
	p.gameData.TableID.Store(nextTableID)
}

// Desc 打印当前玩家的关键游戏状态（调试用）
func (p *Player) Desc() string {
	return fmt.Sprintf("(%d %d T:%d St:%s robot:%t co:%v ids=%v dices:%v)",
		p.GetPlayerID(), p.GetChairID(), p.GetTableID(), p.GetStatus().String(), p.isRobot,
		p.GetColor(), xgo.ToJSON(p.GetPieces()), xgo.ToJSON(p.DiceListInt32()))
}

func (p *Player) SetTableID(tableID int32) {
	p.gameData.TableID.Store(tableID)
}

func (p *Player) GetTableID() int32 {
	return p.gameData.TableID.Load()
}

func (p *Player) SetChairID(chairID int32) {
	p.gameData.ChairID.Store(chairID)
}

func (p *Player) GetChairID() int32 {
	return p.gameData.ChairID.Load()
}

func (p *Player) IncrTimeoutCnt(timeout bool) {
	if timeout {
		p.gameData.idleCount++
	}
}

func (p *Player) ClearTimeoutCnt() {
	p.gameData.idleCount = 0
}

func (p *Player) GetTimeoutCnt() int32 {
	return p.gameData.idleCount
}

func (p *Player) SetOffline(v bool) {
	p.gameData.isOffline.Store(v)
}

func (p *Player) IsOffline() bool {
	return p.gameData.isOffline.Load()
}

func (p *Player) GetStatus() Status {
	return p.gameData.status
}

func (p *Player) SetSit() {
	p.gameData.status = StSit
}

func (p *Player) SetReady() {
	p.gameData.status = StReady
}

func (p *Player) IsReady() bool {
	return p.gameData.status == StReady
}

func (p *Player) SetGaming() {
	p.gameData.status = StGaming
}

func (p *Player) IsGaming() bool {
	return p.gameData.status == StGaming
}

func (p *Player) StartGame(stake float64) {
	p.baseData.Money -= stake
	p.InitDicesSequence()
}

func (p *Player) SetColor(color int32) {
	p.gameData.color = color
}

func (p *Player) GetColor() int32 {
	return p.gameData.color
}

func (p *Player) SetPieces(pieces []int32) {
	p.gameData.pieceIDs = pieces
}

func (p *Player) GetPieces() []int32 {
	return p.gameData.pieceIDs
}

func (p *Player) MarkPieceArrived(pieceID int32) {
	for k, id := range p.GetPieces() {
		if pieceID == id {
			p.gameData.pieceIDs[k] = -id
			return
		}
	}
}

// InitDicesSequence 生成 n+1 个骰子序列，保证前 n 次至少有一个 6
func (p *Player) InitDicesSequence() {
	if conf.FastMode {
		return
	}

	const n = 3

	// 1. [1,5] 洗牌
	base := []int32{1, 2, 3, 4, 5}
	xgo.SliceShuffle(base)

	// 2. 抽出前 n+1 个
	seq := make([]int32, n+1)
	copy(seq, base[:n+1])

	// 3. 保证前 n 里有一个 6
	seq[xgo.RandInt(0, n)] = 6

	// 4. 最终赋值
	p.gameData.initDiceList = seq
}

// AddDice 添加一个骰子（未使用状态）
func (p *Player) AddDice(value int32) {
	p.gameData.dices = append(p.gameData.dices, DiceSlot{Value: value, Used: false})

	// 更新最新骰子
	p.gameData.lastRoll = value
}

// GetDiceSlot 返回当前骰子列表（含已用/未用）
func (p *Player) GetDiceSlot() []DiceSlot { return p.gameData.dices }

// GetLastDiceSlot 返回上轮骰子记录
func (p *Player) GetLastDiceSlot() []DiceSlot { return p.gameData.lastDices }

// UnusedDice 返回当前未被使用的骰子点数
func (p *Player) UnusedDice() []int32 {
	var out []int32
	for _, d := range p.gameData.dices {
		if !d.Used {
			out = append(out, d.Value)
		}
	}
	return out
}

// HasUnusedDice 检查是否存在某个未用骰子
func (p *Player) HasUnusedDice(value int32) bool {
	for _, d := range p.gameData.dices {
		if d.Value == value && !d.Used {
			return true
		}
	}
	return false
}

// UseDice 标记某个骰子为已使用
func (p *Player) UseDice(value int32) bool {
	for i, d := range p.gameData.dices {
		if d.Value == value && !d.Used {
			p.gameData.dices[i].Used = true
			return true
		}
	}
	return false
}

// DiceListInt32 返回当前骰子列表，使用负数表示已使用
func (p *Player) DiceListInt32() []int32 {
	dices := make([]int32, 0, len(p.gameData.dices))
	for _, d := range p.gameData.dices {
		val := d.Value
		if d.Used {
			val = -val
		}
		dices = append(dices, val)
	}
	return dices
}

// IsTripleSix 检测是否连续投出三个未使用的6（跳过回合）
func (p *Player) IsTripleSix() bool {
	count := 0
	dices := p.gameData.dices
	for i := len(dices) - 1; i >= 0 && count < tripleRollLimit; i-- {
		if dices[i].Used {
			continue
		}
		if dices[i].Value != maximumDiceValue {
			break
		}
		count++
	}
	return count >= tripleRollLimit
}

// FinishTurn 回合结束，记录当前骰子为 lastDices 并清空 dices
func (p *Player) FinishTurn() {
	p.gameData.lastDices = make([]DiceSlot, len(p.gameData.dices))
	copy(p.gameData.lastDices, p.gameData.dices)
	p.gameData.dices = nil
}

func (p *Player) SetFinish() {
	p.gameData.allArrived = true
}

func (p *Player) IsFinish() bool {
	return p.gameData.allArrived
}

func (p *Player) GetLastRoll() int32 {
	return p.gameData.lastRoll
}

func (p *Player) RollInitDiceList() int32 {
	if len(p.gameData.initDiceList) == 0 {
		return -1
	}
	dice := p.gameData.initDiceList[0]
	p.gameData.initDiceList = p.gameData.initDiceList[1:]
	return dice
}
