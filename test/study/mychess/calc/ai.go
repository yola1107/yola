package calc

import (
	"yola/test/study/base"
)

const (
	_level = 4
)

type aiTest struct {
	nCamp int32
	cMap  *Map
}

func NewAiTest(nCamp int32, cMap *Map) *aiTest {
	return &aiTest{
		nCamp: nCamp,
		cMap:  cMap,
	}
}

//移动
func (a *aiTest) Move() *Step {
	if step := a.bestMove(); step != nil {
		if a.cMap.CanMove(step.moveId, step.toRow, step.toCol) {
			a.cMap.MoveStone(step.moveId, step.killId, step.toRow, step.toCol)
			return step
		}
	}
	return nil
}

//获取最佳路径
func (a *aiTest) bestMove() *Step {
	var best *Step
	var maxInAllMinScore = int32(-100000)

	var steps = a.allCanMoveStep()
	for _, step := range steps {
		a.fakeMove(step)

		//minScore := a.calcScore()	//算分
		minScore := a.getMinScore(_level-1, maxInAllMinScore) //优化算分

		a.unFakeMove(step)

		if minScore > maxInAllMinScore {
			best = step
			maxInAllMinScore = minScore
		}
	}
	return best
}

func (a *aiTest) getMinScore(level, curMinScore int32) int32 {
	if level == 0 {
		return a.calcScore()
	}

	var steps = a.allCanMoveStep()
	var minInAllMaxScore = int32(1000000)

	for _, step := range steps {
		a.fakeMove(step)

		maxScore := a.getMaxScore(level-1, minInAllMaxScore)

		a.unFakeMove(step)

		if maxScore <= curMinScore {
			return maxScore
		}

		if maxScore < minInAllMaxScore {
			minInAllMaxScore = maxScore
		}
	}

	return minInAllMaxScore
}

func (a *aiTest) getMaxScore(level, curMaxScore int32) int32 {
	if level == 0 {
		return a.calcScore()
	}

	var steps = a.allCanMoveStep()
	var maxInAllMinScore = int32(-1000000)

	for _, step := range steps {
		a.fakeMove(step)

		minScore := a.getMinScore(level-1, maxInAllMinScore)

		a.unFakeMove(step)

		if minScore >= curMaxScore {
			return minScore
		}

		if minScore > maxInAllMinScore {
			maxInAllMinScore = minScore
		}
	}

	return maxInAllMinScore
}

//假装走一步
func (a *aiTest) fakeMove(step *Step) {
	a.cMap.MoveStone(step.moveId, step.killId, step.toRow, step.toCol)
}

//回退假装走的那一步
func (a *aiTest) unFakeMove(step *Step) {
	a.cMap.backOne()
	//a.cMap.stones[step.moveId].nRow = step.fromRow
	//a.cMap.stones[step.moveId].nCol = step.fromCol
	//a.cMap.reliveStone(step.killId)
	//a.cMap.beRedTurn = !a.cMap.beRedTurn
}

func (a *aiTest) allCanMoveStep() []*Step {
	var steps = []*Step(nil)
	var min, max = 0, 16

	if !a.cMap.beRedTurn {
		min, max = 16, 32
	}

	//遍历己方所有活棋子寻找可行路径
	for i := min; i < max; i++ {
		if a.cMap.stones[i].bDead {
			continue
		}
		for row := int32(0); row < _maxRow; row++ {
			for col := int32(0); col < _maxCol; col++ {
				if a.cMap.CanMove(int32(i), row, col) {
					var mover = a.cMap.stones[i]
					steps = append(steps, &Step{
						moveId:  int32(i),
						killId:  a.cMap.GetStoneId(row, col),
						fromRow: mover.nRow,
						fromCol: mover.nCol,
						toRow:   row,
						toCol:   col,
					})
				}
			}
		}
	}

	return steps
}

//计算分数
func (a *aiTest) calcScore() int32 {
	// 权重分 [车  马  炮  兵  士  象  将]
	var _Score = map[int32]int32{
		CHE:   int32(base.RandRange(300, 350)),
		MA:    int32(base.RandRange(150, 200)),
		PAO:   int32(base.RandRange(120, 200)),
		BING:  int32(base.RandRange(10, 50)),
		JIANG: int32(base.RandRange(600, 900)),
		SHI:   int32(base.RandRange(30, 80)),
		XIANG: int32(base.RandRange(30, 80)),
	}

	var redScore = int32(0)
	var blackScore = int32(0)

	for i := 0; i < _maxStoneNum; i++ {
		var s = a.cMap.stones[i]
		if !s.bDead {
			if i < _maxStoneNum/2 {
				redScore += _Score[s.emType] //计算红棋总分
			} else {
				blackScore += _Score[s.emType] //计算黑棋总分
			}
		}
	}

	score := redScore - blackScore
	if a.nCamp == CampBLACK {
		score = -score
	}

	return score
}
