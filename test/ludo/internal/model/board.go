package model

const (
	MoveOK               int32 = iota // 移动合法，无错误
	ErrInvalidMoveCount               // 移动步数与骰子数量不一致
	ErrInvalidPieceIdx                // 棋子索引无效（ID 不存在或越界）
	ErrInvalidPieceColor              // 棋子颜色与玩家不符
	ErrPieceCannotMove                // 棋子状态不可移动（如已到终点）
	ErrDiceMismatch                   // 移动步数与骰子点数不匹配
	ErrInvalidStep                    // 非法步数（如 <= 0）
	ErrIdleMustBeSix                  // 起始状态只能掷出 6 才能出发
	ErrExceedHomePath                 // 超出终点路径，无法移动
	ErrAlreadyArrived                 // 棋子已到达终点，不能再移动
	ErrMovesIncomplete                // 有未使用的骰子点数，移动不完整
)

const maxTrackedSteps = 10

// Board 代表一个Ludo棋盘，管理棋子、步骤和颜色映射
type Board struct {
	fastMode bool     // 游戏模式，false=经典，true=快速
	pieces   []*Piece // 所有棋子，索引即棋子ID
	steps    []*Step  // 执行过的移动步骤记录
}

// NewBoard 创建新的棋盘，seatColors为玩家颜色数组，每色棋子数量piecesPerSeat，isFastMode标记快速模式
func NewBoard(seatColors []int32, piecesPerSeat int, isFastMode bool) *Board {
	b := &Board{
		fastMode: isFastMode,
	}

	var id int32
	for _, color := range seatColors {
		for i := 0; i < piecesPerSeat; i++ {
			p := NewPiece(id, color, isFastMode)
			b.pieces = append(b.pieces, p)
			id++
		}
	}
	return b
}

// Pieces 返回所有棋子指针切片
func (b *Board) Pieces() []*Piece {
	return b.pieces
}

// GetPieceByID 根据ID获取棋子，ID越界返回nil
func (b *Board) GetPieceByID(id int32) *Piece {
	if id < 0 || int(id) >= len(b.pieces) {
		return nil
	}
	return b.pieces[id]
}

// GetPieceIDsByColor 返回指定颜色所有棋子ID切片
func (b *Board) GetPieceIDsByColor(color int32) []int32 {
	var ids []int32
	for _, p := range b.pieces {
		if p != nil && p.color == color {
			ids = append(ids, p.id)
		}
	}
	return ids
}

// GetActivePieceIDs 返回指定颜色且未到终点的棋子ID列表
func (b *Board) GetActivePieceIDs(color int32) []int32 {
	var active []int32
	for _, p := range b.pieces {
		if p != nil && p.color == color && !p.IsArrived() {
			active = append(active, p.id)
		}
	}
	return active
}

// Clone 深拷贝棋盘当前状态，不复制仅供回溯的步骤记录。
func (b *Board) Clone() *Board {
	c := &Board{
		fastMode: b.fastMode,
		pieces:   make([]*Piece, len(b.pieces)),
	}
	for i, p := range b.pieces {
		if p != nil {
			cp := *p // 值拷贝
			c.pieces[i] = &cp
		}
	}
	return c
}

// Clear 清空引用
func (b *Board) Clear() {
	if b == nil {
		return
	}

	for i := range b.pieces {
		b.pieces[i] = nil
	}
	b.pieces = nil

	for i := range b.steps {
		b.steps[i].Killed = nil // 清理内部切片
		b.steps[i] = nil
	}
	b.steps = nil
}

// Move 单步移动，返回步骤信息
func (b *Board) Move(pieceID, steps int32) *Step {
	return b.moveOne(pieceID, steps)
}

// CanMove 判断指定颜色棋子是否可以移动指定步数，返回能否移动及错误码
func (b *Board) CanMove(color, pieceID, x int32) (bool, int32) {
	// 校验持方与移动棋子的持方一致
	p := b.GetPieceByID(pieceID)
	if p == nil {
		return false, ErrInvalidPieceIdx
	}
	if p.color != color {
		return false, ErrInvalidPieceColor
	}
	// 校验是否真的可以移动
	if ok, code, _ := calcNextPos(p.pos, p.color, x); !ok {
		return false, code
	}
	return true, MoveOK
}

// canMoveOne 判断单个棋子是否能移动指定步数
func (b *Board) canMoveOne(id, x int32) (bool, int32) {
	p := b.GetPieceByID(id)
	if p == nil {
		return false, ErrInvalidPieceIdx
	}
	if ok, code, _ := calcNextPos(p.pos, p.color, x); !ok {
		return false, code
	}
	return true, MoveOK
}

// moveOne 执行单个棋子移动，返回该步详细信息
func (b *Board) moveOne(id, x int32) *Step {
	p := b.GetPieceByID(id)
	if p == nil {
		return nil
	}
	from, to := p.move(x)
	step := &Step{ID: id, From: from, To: to, X: x, Color: p.color, Killed: nil}
	if from != to {
		killed := p.canKillAt(b.pieces, to)
		for _, v := range killed {
			// 被击杀后的落点. class模式回到基地, fast模式回到起始点
			kFrom := v.pos
			v.setPos(calcBasePos(v.color, b.fastMode))
			step.Killed = append(step.Killed, KilledInfo{
				ID:    v.id,
				From:  kFrom,
				To:    v.pos,
				Color: v.color,
			})
		}
	}
	b.steps = append(b.steps, step)
	if len(b.steps) > maxTrackedSteps {
		copy(b.steps, b.steps[len(b.steps)-maxTrackedSteps:])
		b.steps = b.steps[:maxTrackedSteps]
	}
	return step
}

// backOne 撤销最后一步移动，恢复棋子及被击杀棋子状态
func (b *Board) backOne() *Step {
	if len(b.steps) == 0 {
		return nil
	}
	step := b.steps[len(b.steps)-1]

	// 撤销移动
	if p := b.GetPieceByID(step.ID); p != nil {
		p.setPos(step.From)
	}

	// 撤销击杀
	for _, killed := range step.Killed {
		if p := b.GetPieceByID(killed.ID); p != nil {
			p.setPos(killed.From)
		}
	}

	// 移除最后一步
	b.steps = b.steps[:len(b.steps)-1]
	return step
}

// CalcCanMoveDice 是否可移动
func (b *Board) CalcCanMoveDice(color int32, dices []int32) bool {
	pieces := b.GetActivePieceIDs(color)
	for _, dice := range dices {
		for _, id := range pieces {
			if can, _ := b.canMoveOne(id, dice); can {
				return true
			}
		}
	}
	return false
}

// CalcAllMovable 根据颜色和骰子列表，计算每个骰子能移动的棋子集合
func (b *Board) CalcAllMovable(color int32, dices []int32) [][]int32 {
	all := make([][]int32, 0)

	pieces := b.GetActivePieceIDs(color)
	for _, dice := range dices {
		for _, id := range pieces {
			if can, _ := b.canMoveOne(id, dice); can {
				all = append(all, []int32{id, dice})
			}
		}
	}
	return all
}
