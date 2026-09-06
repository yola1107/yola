package table

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"yola/test/internal/filelog"
	"yola/test/internal/xgo"
	"yola/test/ludo/api/v1"
	"yola/test/ludo/internal/biz/player"
	"yola/test/ludo/internal/conf"
	"yola/test/ludo/internal/model"
)

const tableLogDirectoryEnv = "YOLA_TEST_LOG_DIR"

type Log struct {
	tableID int32
	sink    *filelog.Log
}

func newTableLog(instanceID string, c *conf.Room_LogCache) *Log {
	logger := new(Log)
	if c == nil || !c.Open {
		return logger
	}
	path := tableLogPath(instanceID)
	sink, err := filelog.New(path)
	if err != nil {
		slog.Error("create table log", "path", path, "error", err)
		return logger
	}
	logger.sink = sink
	return logger
}

func tableLogPath(instanceID string) string {
	directory := strings.TrimSpace(os.Getenv(tableLogDirectoryEnv))
	if directory == "" {
		directory = "logs"
	}
	escapedInstanceID := url.PathEscape(strings.TrimSpace(instanceID))
	if escapedInstanceID == "" {
		escapedInstanceID = "unknown"
	}
	return filepath.Join(directory, "log_cache", conf.Name, "instance-"+escapedInstanceID, "tables.log")
}

func (l *Log) forTable(tableID int32) *Log {
	return &Log{tableID: tableID, sink: l.sink}
}

func (l *Log) Close() error {
	if l == nil || l.sink == nil {
		return nil
	}
	return l.sink.Close()
}

func (l *Log) write(message string, args ...any) {
	if !l.enabled() {
		return
	}
	values := make([]any, 0, len(args)+1)
	values = append(values, l.tableID)
	values = append(values, args...)
	if err := l.sink.Write("[table_id=%d] "+message, values...); err != nil {
		slog.Error("write table log", "table_id", l.tableID, "error", err)
	}
}

func (l *Log) enabled() bool { return l != nil && l.sink != nil }

func debugLogEnabled() bool {
	return slog.Default().Enabled(context.Background(), slog.LevelDebug)
}

func (l *Log) writePlayer(p *player.Player, message string, args ...any) {
	if !l.enabled() {
		return
	}
	values := make([]any, 0, len(args)+1)
	values = append(values, p.Desc())
	values = append(values, args...)
	l.write(message, values...)
}

func (l *Log) userEnter(p *player.Player, seatCount int32) {
	l.writePlayer(p, "[进入房间] 玩家:%+v 桌子人数(%+v) ", seatCount)
}

func (l *Log) userReEnter(p *player.Player, seatCount int32) {
	l.writePlayer(p, "[重进房间] 玩家:%+v 桌子人数(%+v) ", seatCount)
}

func (l *Log) userExit(p *player.Player, seatCount int32, lastChair int32, isSwitchTable bool) {
	l.writePlayer(p, "[离开房间] 玩家:%+v 桌子人数(%+v) lastChair(%d) 是否换桌(%+v) ", seatCount, lastChair, isSwitchTable)
}

func (l *Log) offline(p *player.Player) { l.writePlayer(p, "【玩家断线】玩家:%+v ") }

func (l *Log) begin(table string, bet float64, seats []*player.Player, infos any) {
	if !l.enabled() {
		return
	}
	logs := []string{fmt.Sprintf("[游戏开始] %s bet:%.1f Gamer=%v", table, bet, infos)}
	for _, p := range seats {
		if p != nil {
			logs = append(logs, fmt.Sprintf("玩家:%+v 投注[%+v] color=%v 状态:%v", p.Desc(), bet, p.GetColor(), p.GetStatus()))
		}
	}
	l.write("%s", strings.Join(logs, "\r\n"))
}

func (l *Log) activePush(p *player.Player, canAction v1.ACTION_TYPE, canMove, result string) {
	l.writePlayer(p, "[操作通知] 玩家:%+v canAction=%q, canMoveDice=%s, Ret=%v", canAction, canMove, result)
}

func (l *Log) stage(stage string, active int32) {
	l.write("[状态转移] %s. active=%+v", stage, active)
}

func (l *Log) Dice(p *player.Player, dice int32, movable bool, timeout bool) {
	l.writePlayer(p, "[玩家掷骰] 玩家:%+v. dice=%d, movable=%v, timeout=%+v", dice, movable, timeout)
}

func (l *Log) Move(p *player.Player, step *model.Step, arrived, timeout bool) {
	if !l.enabled() {
		return
	}
	pieceID, diceValue := int32(0), int32(0)
	if step != nil {
		pieceID, diceValue = step.ID, step.X
	}
	eat := []int32(nil)
	if step != nil && len(step.Killed) > 0 {
		for _, killed := range step.Killed {
			eat = append(eat, killed.ID)
		}
	}
	eatDescription := ""
	if len(eat) > 0 {
		eatDescription = fmt.Sprintf("(cnt=%d,e=%v)", len(eat), eat)
	}
	l.writePlayer(p, "[玩家移动] 玩家:%+v. [id=%d, x=%d], isArrived=%v, eat=%s, step=%v, timeout=%+v",
		pieceID, diceValue, arrived, eatDescription, xgo.ToJSON(step), timeout)
}

func logPlayers(players []*player.Player) string {
	logs := []string{""}
	for _, p := range players {
		if p != nil {
			logs = append(logs, fmt.Sprintf("<玩家>:%+v 状态:%v", p.Desc(), p.GetStatus()))
		}
	}
	return strings.Join(logs, "\r\n")
}

func (l *Log) end(message string) {
	l.write("[GameEnd] %s", message)
	l.write("\r\n\r\n\r\n")
}
