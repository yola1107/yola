package model

import "math"

const (
	threatenedDistance = int32(6)
	dangerousDistance  = int32(6)
)

type moveSearch struct {
	board        *Board
	color        int32
	dices        []int32
	usedDices    []bool
	pieces       []int32
	enemies      []int32
	totalDiceSum int32
	bestScore    int32
	bestPath     []int32
	maxSteps     int
}

// FindBestMoveSequence 返回最佳移动序列第一步
func FindBestMoveSequence(b *Board, dices []int32, color int32) (int32, int32) {
	if b == nil || len(dices) == 0 {
		return -1, -1
	}

	boardCopy := b.Clone()

	var ids, enemy []int32
	for _, p := range boardCopy.Pieces() {
		if p == nil || p.IsArrived() {
			continue
		}
		if p.color == color {
			ids = append(ids, p.id)
		} else {
			enemy = append(enemy, p.id)
		}
	}
	if len(ids) == 0 {
		return -1, -1
	}

	totalDiceSum := int32(0)
	for _, d := range dices {
		totalDiceSum += d
	}
	search := &moveSearch{
		board:        boardCopy,
		color:        color,
		dices:        dices,
		usedDices:    make([]bool, len(dices)),
		pieces:       ids,
		enemies:      enemy,
		totalDiceSum: totalDiceSum,
		bestScore:    math.MinInt32,
	}
	search.evaluate(len(dices), make([]int32, 0, len(dices)*2), make([]*Step, 0, len(dices)))

	if len(search.bestPath) > 1 {
		return search.bestPath[0], search.bestPath[1]
	}
	return -1, -1
}

// evaluate 遍历所有移动序列，同时计算分数，避免生成所有路径。
func (s *moveSearch) evaluate(remainingDice int, path []int32, steps []*Step) {
	// 剪枝 1：当前路径 + 剩余骰子 < 当前最大步数，直接回溯
	if len(steps)+remainingDice < s.maxSteps {
		return
	}

	// DFS 递归传下来的 steps 就可以直接评估
	if len(steps) > 0 {
		score := s.board.evaluateMoveSequence(steps, s.totalDiceSum, s.pieces, s.enemies)
		// 1>优先选择步数最多的路径（用完更多骰子）
		// 2>步数相同的情况下选择分数最高的路径
		if len(steps) > s.maxSteps || score > s.bestScore && len(steps) == s.maxSteps {
			s.maxSteps = len(steps)
			s.bestScore = score
			s.bestPath = append(s.bestPath[:0], path...)
		}
	}

	// 遍历每个骰子和棋子
	for diceIndex, dice := range s.dices {
		if s.usedDices[diceIndex] {
			continue
		}
		for _, pieceID := range s.pieces {
			if ok, _ := s.board.canMoveOne(pieceID, dice); !ok {
				continue
			}
			s.usedDices[diceIndex] = true
			path = append(path, pieceID, dice)
			step := s.board.moveOne(pieceID, dice)
			steps = append(steps, step)

			s.evaluate(remainingDice-1, path, steps)

			s.board.backOne()
			path = path[:len(path)-2]
			steps = steps[:len(steps)-1]
			s.usedDices[diceIndex] = false
		}
	}
}

// evaluateMoveSequence 评估移动序列的得分函数，考虑击杀数、移动距离、特殊奖励和惩罚
func (b *Board) evaluateMoveSequence(steps []*Step, totalDiceSum int32, ids, enemy []int32) int32 {
	score := int32(0)
	usedDiceSum := int32(0)

	for i, step := range steps {
		if step.From == step.To {
			continue
		}
		score += step.X * 2 // 基础移动奖励
		usedDiceSum += step.X

		// 击杀奖励
		for _, killed := range step.Killed {
			score += StepsFromStart(killed.From, killed.Color)*2 + int32(len(steps)-i)*2 + 20
		}

		// 出基地奖励
		if step.From == BasePos {
			score += 60
		}

		// 到达终点奖励
		if mover := b.GetPieceByID(step.ID); mover != nil && mover.IsArrived() {
			score += 80
		}
	}

	// 危险/威胁
	for _, id := range ids {
		p := b.GetPieceByID(id)
		if p == nil {
			continue
		}
		if _, safe := SafePositions[p.pos]; safe {
			score += 15 // 占安全点轻微加分
		}
		if p.state == PieceInHomePath {
			score += 2 // 家路径轻微加分
		}

		if p.IsOnBoard() {
			// 越靠近终点，奖励越高（尽量推进）
			stepsToEnd := (p.pos - HomeEntrances[p.color] + TotalPositions) % TotalPositions
			score += (TotalPositions - stepsToEnd) / 5

			// 危险/威胁
			for _, eID := range enemy {
				e := b.GetPieceByID(eID)
				if !e.IsOnBoard() || !p.IsEnemy(e) {
					continue
				}

				forwardDist := (e.pos - p.pos + TotalPositions) % TotalPositions  // p到e的顺时针距离
				backwardDist := (p.pos - e.pos + TotalPositions) % TotalPositions // e到p的顺时针距离

				// 危险：敌人在我后面，可能追上来
				if backwardDist > 0 && backwardDist <= dangerousDistance {
					score -= (dangerousDistance - backwardDist + 1) * 2 // 越近惩罚越大
				}

				// 威胁：我在敌人后面，可以追杀
				if forwardDist > 0 && forwardDist <= threatenedDistance {
					score += (threatenedDistance - forwardDist + 1) / 2 // 越近奖励越大
				}
			}
		}
	}

	// 奖励用完点数
	score += usedDiceSum

	// 惩罚浪费
	if wasted := totalDiceSum - usedDiceSum; wasted > 0 {
		score -= wasted
	}

	return score
}
