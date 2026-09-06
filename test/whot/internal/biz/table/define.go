package table

import (
	"fmt"
	"log/slog"
	"time"

	"yola/test/whot/api/v1"
	"yola/test/whot/internal/biz/player"
)

// StageID 表示游戏阶段。
type StageID int32

const (
	StWait     StageID = iota // 等待
	StReady                   // 准备
	StSendCard                // 发牌
	StPlaying                 // 操作
	StWaitEnd                 // 等待结束
	StEnd                     // 游戏结束
)

var stageTimeouts = map[StageID]int64{
	StWait:     0,
	StReady:    0,
	StSendCard: 3,
	StPlaying:  8,
	StWaitEnd:  3,
	StEnd:      10,
}

var stageNames = map[StageID]string{
	StWait:     "StWait",
	StReady:    "StReady",
	StSendCard: "StSendCard",
	StPlaying:  "StPlaying",
	StWaitEnd:  "StWaitEnd",
	StEnd:      "StEnd",
}

// String returns the string representation of the StageID.
func (s StageID) String() string {
	if name, ok := stageNames[s]; ok {
		return name
	}
	return fmt.Sprintf("StageID(%d)", s)
}

// Timeout returns the timeout duration of the stage.
func (s StageID) Timeout() int64 {
	if timeout, ok := stageTimeouts[s]; ok {
		return timeout
	}
	slog.Warn("unknown stage", "stage", s, "default_timeout", "0s")
	return 0
}

// Stage 封装当前阶段及其定时信息。
type Stage struct {
	state      StageID
	prev       StageID
	timerID    int64
	generation uint64
	startAt    time.Time
	duration   time.Duration
}

func (s *Stage) Remaining() time.Duration {
	elapsed := time.Since(s.startAt)
	if elapsed > s.duration {
		return 0
	}
	return s.duration - elapsed
}

func (s *Stage) State() StageID { return s.state }

func (s *Stage) TimerID() int64 { return s.timerID }

func (s *Stage) Generation() uint64 { return s.generation }

func (s *Stage) Desc() string {
	return fmt.Sprintf("[%v->%+v, %+v -> %v, dur=%v]",
		int32(s.prev), int32(s.state), s.prev, s.state, s.duration)
}

func (s *Stage) Set(state StageID, duration time.Duration, timerID int64) uint64 {
	s.prev = s.state
	s.state = state
	s.startAt = time.Now()
	s.duration = duration
	s.timerID = timerID
	s.generation++
	return s.generation
}

func (s *Stage) SetTimerID(generation uint64, timerID int64) {
	if s.generation == generation {
		s.timerID = timerID
	}
}

// SettleObj 按 BaseScore + totalLost*(1-TaxRate) 计算赢家所得，其他玩家各扣 BaseScore。
type SettleObj struct {
	Winner    *player.Player
	Users     []*player.Player
	BaseScore float64
	TaxRate   float64
	EndType   v1.FINISH_TYPE

	TaxFee   float64
	WinScore float64
	result   *v1.ResultPush
}

// GetResult 返回结算结果（只读）
func (s *SettleObj) GetResult() *v1.ResultPush {
	return s.result
}

// Settle 执行结算
func (s *SettleObj) Settle() error {
	if s.Winner == nil || len(s.Users) == 0 {
		return fmt.Errorf("invalid settle input: winner or users missing")
	}
	if s.BaseScore < 0 || s.TaxRate < 0 || s.TaxRate > 1 {
		return fmt.Errorf("invalid baseScore or taxRate")
	}

	winID := s.Winner.GetPlayerID()
	var (
		totalLost float64
		results   []*v1.PlayerResult
	)

	for _, p := range s.Users {
		if p == nil || p.GetPlayerID() == winID {
			continue
		}
		totalLost += s.BaseScore
		results = append(results, buildResult(p, false, -s.BaseScore))
	}

	tax := totalLost * s.TaxRate
	winScore := s.BaseScore + totalLost - tax
	s.WinScore = winScore
	s.TaxFee = tax

	results = append(results, buildResult(s.Winner, true, winScore))

	s.result = &v1.ResultPush{
		FinishType: s.EndType,
		WinnerID:   winID,
		Results:    results,
	}
	return nil
}

func buildResult(p *player.Player, isWinner bool, score float64) *v1.PlayerResult {
	return &v1.PlayerResult{
		UserID:         p.GetPlayerID(),
		ChairID:        p.GetChairID(),
		IsWinner:       isWinner,
		WinScore:       score,
		HandCards:      p.GetCards(),
		HandCardsScore: p.GetHandScore(),
	}
}
