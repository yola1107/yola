package table

import (
	"fmt"
	"log/slog"
	"time"

	"yola/test/ludo/internal/conf"
)

// StageID 标识游戏阶段。
type StageID int32

const (
	StWait     StageID = iota // 等待
	StReady                   // 准备
	StSendCard                // 发牌
	StDice                    // 投掷色子
	StMove                    // 移动棋子
	StResult                  // 结算
)

const fastModeMoveTimeoutSeconds = 5

var stages = map[StageID]struct {
	name    string
	timeout int64
}{
	StWait:     {name: "StWait"},
	StReady:    {name: "StReady"},
	StSendCard: {name: "StSendCard", timeout: 3},
	StDice:     {name: "StDice", timeout: 7},
	StMove:     {name: "StMove", timeout: 7},
	StResult:   {name: "StResult", timeout: 3},
}

func (s StageID) String() string {
	if stage, ok := stages[s]; ok {
		return stage.name
	}
	return fmt.Sprintf("StageID(%d)", s)
}

func (s StageID) Timeout() int64 {
	stage, ok := stages[s]
	if !ok {
		slog.Warn("unknown stage", "stage", int32(s), "fallback_timeout_seconds", 0)
		return 0
	}
	if conf.FastMode && (s == StDice || s == StMove) {
		return fastModeMoveTimeoutSeconds
	}
	return stage.timeout
}

// Stage 封装当前阶段及其定时信息。
type Stage struct {
	state      StageID
	prev       StageID
	timerID    int64
	generation uint64
	startedAt  time.Time
	duration   time.Duration
}

func (s *Stage) Remaining() time.Duration {
	elapsed := time.Since(s.startedAt)
	if elapsed > s.duration {
		return 0
	}
	return s.duration - elapsed
}

func (s *Stage) State() StageID {
	return s.state
}

func (s *Stage) TimerID() int64 {
	return s.timerID
}

func (s *Stage) Generation() uint64 {
	return s.generation
}

func (s *Stage) Desc() string {
	return fmt.Sprintf("[%v->%+v, %+v -> %v, dur=%v]",
		int32(s.prev), int32(s.state), s.prev, s.state, s.duration)
}

func (s *Stage) Set(state StageID, duration time.Duration, timerID int64) uint64 {
	s.prev = s.state
	s.state = state
	s.startedAt = time.Now()
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
