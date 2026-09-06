package model

// PieceState 表示棋子状态
type PieceState int32

const (
	PieceIdle       PieceState = iota // 未出发
	PieceOnBoard                      // 在公共路径上
	PieceInHomePath                   // 在终点路径上
	PieceArrived                      // 到达终点
)

const (
	ColorCount           = 4  // 4种棋子颜色
	TotalPositions       = 52 // 公共路径长度
	HomePathLen          = 6  // Home路径长度
	BasePos        int32 = -1 // 基地点pos
)

const maximumDiceValue = int32(6)

var (
	EntryPoints      = []int32{0, 13, 26, 39}      // 四个入口点
	HomeEntrances    = []int32{50, 11, 24, 37}     // 四个回家入口点
	HomeStartIndices = []int32{101, 111, 121, 131} // 四个Home路径起始点
	SafePositions    = map[int32]struct{}{         // 八个安全点（不可被击杀）
		0: {}, 8: {}, 13: {}, 21: {}, 26: {}, 34: {}, 39: {}, 47: {},
	}
)

// Piece 代表一枚棋子
type Piece struct {
	id    int32
	color int32
	pos   int32
	state PieceState
}

func NewPiece(id, color int32, isFastMode bool) *Piece {
	if isFastMode {
		return &Piece{id: id, color: color, pos: EntryPoints[color], state: PieceOnBoard}
	}
	return &Piece{id: id, color: color, pos: BasePos, state: PieceIdle}
}

func (p *Piece) ID() int32             { return p.id }
func (p *Piece) Color() int32          { return p.color }
func (p *Piece) Pos() int32            { return p.pos }
func (p *Piece) Status() int32         { return int32(p.state) }
func (p *Piece) IsOnBoard() bool       { return p.state == PieceOnBoard }
func (p *Piece) IsArrived() bool       { return p.state == PieceArrived }
func (p *Piece) IsEnemy(o *Piece) bool { return p.color != o.color }

// calcBasePos 计算初始点. class模式初始点在基地. fast模式初始点在起始点(出基地的第一格)
func calcBasePos(color int32, isFastMode bool) int32 {
	if isFastMode {
		return EntryPoints[color]
	}
	return BasePos
}

// setPos 更新位置，自动更新状态
func (p *Piece) setPos(pos int32) {
	p.pos = pos
	p.state = calcStateByPos(pos, p.color)
}

// canKillAt 检查目标位置能否击杀敌方棋子（非安全区）
// 返回被击杀棋子列表（可多枚）
func (p *Piece) canKillAt(pieces []*Piece, targetPos int32) []*Piece {
	if p.state != PieceOnBoard {
		return nil
	}
	if _, safe := SafePositions[targetPos]; safe {
		return nil
	}
	var kills []*Piece
	for _, other := range pieces {
		if other != p && other.pos == targetPos && other.IsOnBoard() && p.IsEnemy(other) {
			kills = append(kills, other)
		}
	}
	return kills
}

// move 移动x
func (p *Piece) move(x int32) (from, to int32) {
	from = p.pos
	canMove, _, newPos := calcNextPos(p.pos, p.color, x)
	if !canMove {
		return from, from
	}
	p.setPos(newPos)
	return from, newPos
}

// 根据位置和颜色计算棋子状态
func calcStateByPos(pos, color int32) PieceState {
	if pos == BasePos {
		return PieceIdle
	}

	homeStart := HomeStartIndices[color]
	homeEnd := homeStart + HomePathLen - 1

	switch {
	case pos == homeEnd:
		return PieceArrived
	case pos >= homeStart && pos < homeEnd:
		return PieceInHomePath
	case pos == HomeEntrances[color]:
		return PieceOnBoard
	case pos >= 0 && pos < TotalPositions:
		return PieceOnBoard
	default:
		// 兜底，防止非法pos
		return PieceIdle
	}
}

// 核心计算函数：计算移动步数后的新位置
// 返回是否可移动，错误码，目标位置
func calcNextPos(pos, color, x int32) (bool, int32, int32) {
	if x <= 0 || x > maximumDiceValue {
		return false, ErrInvalidStep, pos
	}

	homeStart := HomeStartIndices[color]
	homeEnd := homeStart + HomePathLen - 1
	entry := HomeEntrances[color]
	entryPoint := EntryPoints[color]

	switch {
	case pos == BasePos:
		if x == maximumDiceValue {
			return true, MoveOK, entryPoint
		}
		return false, ErrIdleMustBeSix, pos

	case pos == homeEnd:
		return false, ErrAlreadyArrived, pos

	case pos >= homeStart && pos < homeEnd:
		newPos := pos + x
		if newPos <= homeEnd {
			return true, MoveOK, newPos
		}
		return false, ErrExceedHomePath, pos

	case pos >= 0 && pos < TotalPositions:
		distToEntry := (entry - pos + TotalPositions) % TotalPositions
		if x <= distToEntry {
			newPos := (pos + x) % TotalPositions
			return true, MoveOK, newPos
		}
		homeSteps := x - distToEntry - 1
		if homeSteps >= 0 && homeSteps < HomePathLen {
			return true, MoveOK, homeStart + homeSteps
		}
		return false, ErrExceedHomePath, pos

	default:
		return false, ErrInvalidStep, pos
	}
}

// CanMovePiece rejects inconsistent wire snapshots before applying the shared movement rule.
func CanMovePiece(pos, color, status, steps int32) bool {
	if color < 0 || color >= ColorCount {
		return false
	}
	if PieceState(status) != calcStateByPos(pos, color) {
		return false
	}
	ok, _, _ := calcNextPos(pos, color, steps)
	return ok
}

// StepsFromStart 计算从EntryPoint起的累计步数
func StepsFromStart(pos, color int32) int32 {
	if pos == BasePos {
		return 0
	}

	homeStart := HomeStartIndices[color]

	// Home 路径
	if pos >= homeStart && pos < homeStart+HomePathLen {
		return TotalPositions + (pos - homeStart)
	}

	// 公共路径
	if pos >= 0 && pos < TotalPositions {
		if pos == EntryPoints[color] {
			return 0
		}
		offset := (pos - EntryPoints[color] + TotalPositions) % TotalPositions
		return offset
	}

	return 0
}
