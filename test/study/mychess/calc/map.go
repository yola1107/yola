package calc

import (
    "fmt"
    "log"
    "strings"

    "yola/internal/base"
)

const (
    _maxStoneNum = 32
    _maxRow      = 10
    _maxCol      = 9
)

const (
    _noExistId = -1
    _noAtALine = -1
)

type Map struct {
    stones    []Stone
    steps     []*Step
    beRedTurn bool
}

func NewMap() *Map {
    m := &Map{}
    for i := int32(0); i < _maxStoneNum; i++ {
        var s Stone
        s.init(i)
        m.stones = append(m.stones, s)
    }
    m.beRedTurn = true
    return m
}

func (m *Map) Stones() []Stone {
    return m.stones
}

func (m *Map) Steps() []*Step {
    return m.steps
}

func (m *Map) Clone() *Map {
    dst := &Map{}
    dst.stones = make([]Stone, len(m.stones))
    copy(dst.stones, m.stones)
    return dst
}

func (m *Map) Reset(src *Map) {
    m.stones = make([]Stone, len(src.stones))
    copy(m.stones, src.stones)
}

func (m *Map) BeStone(row, col int32) bool {
    return m.GetStoneId(row, col) != _noExistId
}

func (m *Map) GetStoneId(row, col int32) int32 {
    if s := m.GetStone(row, col); s != nil {
        return s.nId
    }
    return _noExistId
}

func (m *Map) GetStone(row, col int32) *Stone {
    for _, v := range m.stones {
        if !v.bDead && v.nRow == row && v.nCol == col {
            return &v
        }
    }
    return nil
}

func (m *Map) killStone(id int32) {
    if id >= 0 && id < _maxStoneNum {
        m.stones[id].bDead = true
    }
}

func (m *Map) reliveStone(id int32) {
    if id >= 0 && id < _maxStoneNum {
        m.stones[id].bDead = false
    }
}

//获取己方老帅
func (m *Map) getCapital(nCamp int32) (ret *Stone) {
    for _, v := range m.stones {
        if v.nCamp == nCamp && v.emType == JIANG {
            return &v
        }
    }
    return nil
}

func (m *Map) stoneNumAtLine(row1, col1, row2, col2 int32) (ret int) {

    if row1 == row2 {
        min := base.MInInt32(col1, col2)
        max := base.MaxInt32(col1, col2)
        for col := min + 1; col < max; col++ {
            if m.BeStone(row1, col) {
                ret++
            }
        }
        return

    } else if col1 == col2 {
        min := base.MInInt32(row1, row2)
        max := base.MaxInt32(row1, row2)
        for row := min + 1; row < max; row++ {
            if m.BeStone(row, col1) {
                ret++
            }
        }
        return
    }

    return _noAtALine
}

// moveId能否移动到(row,col)
func (m *Map) CanMove(moveId, row, col int32) bool {

    if moveId < 0 || moveId >= _maxStoneNum {
        return false
    }

    if m.stones[moveId].bDead {
        return false
    }

    if m.stones[moveId].nRow == row && m.stones[moveId].nCol == col {
        return false
    }

    // (row,col)存在且为同方的棋子，不可移动
    var killId = m.GetStoneId(row, col)
    if killId != _noExistId && killId/16 == moveId/16 {
        return false
    }

    //能否移动?
    if canMove := m.canMove(moveId, killId, row, col); canMove == false {
        return false
    }

    //真的可以移动么？
    if canMove := m.tryMove(moveId, killId, row, col); canMove == false {
        return false
    }

    //log.Printf("CanMove ok, %s", m.DescMove(moveId, killId, row, col))

    return true
}

// 象棋移动的规则[将  士  象  马  车  炮  兵]
func (m *Map) canMove(moveId, killId, row, col int32) bool {
    switch m.stones[moveId].emType {
    case JIANG:
        return m.canMoveJIANG(moveId, row, col)
    case SHI:
        return m.canMoveSHI(moveId, row, col)
    case XIANG:
        return m.canMoveXIANG(moveId, row, col)
    case MA:
        return m.canMoveMA(moveId, row, col)
    case CHE:
        return m.canMoveCHE(moveId, row, col)
    case PAO:
        return m.canMovePAO(moveId, killId, row, col)
    case BING:
        return m.canMoveBING(moveId, row, col)
    default:
        log.Printf("unknown step key, moveId:%d (%d,%d) mover:%+v ", moveId, row, col, m.stones[moveId])
        return false
    }
}

func (m *Map) canMoveJIANG(moveId, row, col int32) bool {
    var s = m.stones[moveId]

    //对将
    a := m.getCapital(s.nCamp)           //我方老帅
    b := m.getCapital((s.nCamp + 1) % 2) //对方老帅
    if a.nCol == b.nCol {
        if m.stoneNumAtLine(row, col, b.nRow, b.nCol) == 0 {
            return false
        }
    }

    //九宫格
    if m.inNiNe(moveId/16, row, col) {
        var dr = s.nRow - row
        var dc = s.nCol - col
        return base.Abs(dr)+base.Abs(dc) == 1
    }

    return false
}

func (m *Map) canMoveSHI(moveId, row, col int32) bool {
    if m.inNiNe(moveId/16, row, col) {
        var dr = m.stones[moveId].nRow - row
        var dc = m.stones[moveId].nCol - col
        return base.Abs(dr)*10+base.Abs(dc) == 11
    }

    return false
}

func (m *Map) canMoveXIANG(moveId, row, col int32) bool {
    var s = m.stones[moveId]

    if !m.isCrossRiver(s.nCamp, row) {
        var dr = s.nRow - row
        var dc = s.nCol - col
        if base.Abs(dr)*10+base.Abs(dc) != 22 {
            return false
        }
        return !m.BeStone((s.nRow+row)/2, (s.nCol+col)/2)
    }

    return false
}

func (m *Map) canMoveMA(moveId, row, col int32) bool {
    var s = m.stones[moveId]
    var dr = s.nRow - row
    var dc = s.nCol - col
    var mr = (s.nRow + row) / 2
    var mc = (s.nCol + col) / 2

    if base.Abs(dr) == 2 && base.Abs(dc) == 1 {
        return !m.BeStone(mr, s.nCol)
    }
    if base.Abs(dr) == 1 && base.Abs(dc) == 2 {
        return !m.BeStone(s.nRow, mc)
    }

    return false
}

func (m *Map) canMoveCHE(moveId, row, col int32) bool {
    var s = m.stones[moveId]
    return m.stoneNumAtLine(s.nRow, s.nCol, row, col) == 0
}

func (m *Map) canMovePAO(moveId, killId, row, col int32) bool {
    var s = m.stones[moveId]
    var num = m.stoneNumAtLine(s.nRow, s.nCol, row, col)

    if killId == _noExistId {
        return num == 0
    }

    return num == 1
}

func (m *Map) canMoveBING(moveId, row, col int32) bool {
    var s = m.stones[moveId]
    var dr = s.nRow - row
    var dc = s.nCol - col

    if d := base.Abs(dr)*10 + base.Abs(dc); d != 01 && d != 10 {
        return false
    }

    switch s.nCamp {
    case CampRED:
        if s.nRow == 3 || s.nRow == 4 {
            //红兵 未过河
            return s.nCol == col && row-s.nRow == 1
        } else {
            //红兵 过河
            return (col == s.nCol && row > 4) || (row == s.nRow && base.Abs(col-s.nCol) == 1)
        }

    case CampBLACK:
        if s.nRow == 5 || s.nRow == 6 {
            //黑兵 未过河
            return s.nCol == col && row-s.nRow == -1
        } else {
            //黑兵 过河
            return (col == s.nCol && row <= 4) || (row == s.nRow && base.Abs(col-s.nCol) == 1)
        }
    }

    return false
}

func (m *Map) inNiNe(nCamp, row, col int32) bool {
    if !(col >= 3 && col <= 5) {
        return false
    }
    if nCamp == CampRED {
        return row >= 0 && row <= 2
    }
    return row >= 7 && row <= 9
}

func (m *Map) isCrossRiver(nCamp, row int32) bool {
    if nCamp == CampRED {
        return row > 4
    }
    return row <= 4
}

//对将
func (m *Map) isExposed() bool {
    a := m.getCapital(CampRED)
    b := m.getCapital(CampBLACK)
    if a.nCol == b.nCol {
        return m.stoneNumAtLine(a.nRow, a.nCol, b.nRow, b.nCol) == 0
    }
    return false
}

func (m *Map) tryMove(moveId, killId, row, col int32) bool {

    var dead = false

    m.moveOne(moveId, killId, row, col)

    //A方移动后，B方还未移动，A的老帅能被B干掉 	// 两只老帅相对了
    dead = m.killing(m.stones[moveId].nCamp) || m.isExposed()

    m.backOne()

    return !dead
}

func (m *Map) moveOne(moveId, KillId, row, col int32) (step *Step) {
    step = m.saveStep(moveId, KillId, row, col)
    m.killStone(KillId)
    m.stones[moveId].nRow = row
    m.stones[moveId].nCol = col
    m.beRedTurn = !m.beRedTurn
    return
}

func (m *Map) saveStep(moveId, killId, row, col int32) *Step {
    var step = &Step{
        moveId:  moveId,
        killId:  killId,
        fromRow: m.stones[moveId].nRow,
        fromCol: m.stones[moveId].nCol,
        toRow:   row,
        toCol:   col,
    }
    m.steps = append(m.steps, step)
    return step
}

func (m *Map) backOne() (step *Step) {
    if cnt := len(m.steps); cnt > 0 {
        step = m.steps[cnt-1]
        m.stones[step.moveId].nRow = step.fromRow
        m.stones[step.moveId].nCol = step.fromCol
        m.reliveStone(step.killId)
        m.steps = m.steps[:cnt-1]
        m.beRedTurn = !m.beRedTurn
    }
    return
}

func (m *Map) Back() *Step {
    return m.backOne()
}

func (m *Map) MoveStone(moveId, KillId, row, col int32) *Step {
    step := m.moveOne(moveId, KillId, row, col)
    step.action = m.calcAction(moveId, KillId)
    step.canEat = m.calcCanEat(moveId, row, col)
    return step
}

// A正在被将军？
func (m *Map) killing(aCamp int32) bool {

    //A方老帅
    var c = m.getCapital(aCamp)

    //B方棋子
    var min, max = 16, 32
    if aCamp == CampBLACK {
        min, max = 0, 16
    }
    for i := min; i < max; i++ {
        if !m.stones[i].bDead && m.canMove(int32(i), c.nId, c.nRow, c.nCol) {
            return true
        }
    }

    return false
}

// A是否被绝杀
func (m *Map) mustKill(aCamp int32) bool {
    return m.allPossibleMove(aCamp) == 0
}

func (m *Map) allPossibleMove(aCamp int32) int {
    var cnt = 0
    var min, max = 0, 16

    if aCamp == CampBLACK {
        min, max = 16, 32
    }

    //遍历己方所有活棋子
    for i := min; i < max; i++ {
        s := m.stones[i]
        if s.bDead {
            continue
        }
        //遍历所有行坐标
        for row := int32(0); row < _maxRow; row++ {
            //遍历所有列坐标
            for col := int32(0); col < _maxCol; col++ {
                if s.nRow == row && s.nCol == col {
                    continue
                }
                if m.CanMove(int32(i), row, col) {
                    cnt++
                    // return cnt
                }
            }
        }
    }

    log.Printf(">>>>>>>>>>>>>>>>> allPossibleMoveCnt:%d\n", cnt)

    return cnt
}

func (m *Map) calcAction(moveId, killId int32) (doType int32) {

    doType = AcMove

    var mover = m.stones[moveId]

    if killId >= 0 && killId < _maxStoneNum {
        if m.stones[killId].GetEmType() == JIANG {
            doType |= AcKill //普杀对方
        } else {
            doType |= AcEat //吃子
        }
    }

    var enemy = (mover.GetCamp() + 1) % 2
    if killing := m.killing(enemy); killing == true {
        if m.mustKill(enemy) {
            doType |= AcMustKill //绝杀对方
        } else {
            doType |= AcKilling //将对方军
        }
    }

    return
}

// moveId已经在(row,col)位置上，moveId可以吃的棋子有哪些(没有根的)
func (m *Map) calcCanEat(moveId, nRow, nCol int32) map[int32]Stone {
    var eat = map[int32]Stone{}
    // 对方的棋子下标[min,max)
    var min, max int32 = 16, 32
    if moveId/16 == CampBLACK {
        min, max = 0, 16
    }
    return m.canEat(moveId, min, min, max, eat)
}

func (m *Map) canEat(moveId, killId, min, max int32, eat map[int32]Stone) map[int32]Stone {
    if killId >= max {
        return eat
    }

    var kill = m.stones[killId]
    var kr, kc = kill.nRow, kill.nCol

    if (!kill.bDead) && m.CanMove(moveId, kr, kc) {
        //试图去吃
        m.moveOne(moveId, int32(killId), kr, kc)
        eat[kill.nId] = kill

        for j := min; j < max; j++ {
            if m.stones[j].bDead || j == killId {
                continue
            }
            if canMove2 := m.CanMove(int32(j), kr, kc); canMove2 {
                delete(eat, kill.nId) //有根，不能吃
                break
            }
        }

        m.backOne() //回退
    }

    return m.canEat(moveId, killId+1, min, max, eat)
}

//【长将】:走子连续不停将军.
func (m *Map) TryGetKilling(moveId, nRow, nCol int32) (killing bool) {
    if m.CanMove(moveId, nRow, nCol) {
        m.moveOne(moveId, m.GetStoneId(nRow, nCol), nRow, nCol) //试图去吃
        c := m.getCapital((moveId/16 + 1) % 2)                  //对方老帅
        killing = m.CanMove(moveId, c.nRow, c.nCol)             //能杀对方帅？
        m.backOne()                                             //回退
    }
    return
}

//【捉】:走子攻击对方除将帅以外的任何非有根子，并企图于下一步吃去。
func (m *Map) TryGetCanEat(moveId, nRow, nCol int32) (eat map[int32]Stone) {
    eat = map[int32]Stone{}
    if m.CanMove(moveId, nRow, nCol) {
        m.moveOne(moveId, m.GetStoneId(nRow, nCol), nRow, nCol) //吃子
        eat = m.calcCanEat(moveId, nRow, nCol)                  //获取吃列表
        m.backOne()                                             //回退
    }
    return
}

func (m *Map) DescMove(moveId, killId, row, col int32) string {
    var b = strings.Builder{}
    var s = m.stones[moveId]
    if killId == _noExistId {
        b.WriteString(fmt.Sprintf("`%d`%s%s(%d,%d)-->(%d,%d) ", moveId, descCamp[s.nCamp], s.sDesc, s.nRow, s.nCol, row, col))
    } else {
        var k = m.stones[killId]
        b.WriteString(fmt.Sprintf("`%d`%s%s(%d,%d)-->(%d,%d), kill:`%d`%s%s(%d,%d) ", moveId, descCamp[s.nCamp], s.sDesc, s.nRow, s.nCol, row, col, killId, descCamp[k.nCamp], k.sDesc, k.nRow, k.nCol))
    }
    return b.String()
}
