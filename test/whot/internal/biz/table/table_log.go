package table

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"yola/test/internal/filelog"
	"yola/test/internal/xgo"
	"yola/test/whot/api/v1"
	"yola/test/whot/internal/biz/player"
	"yola/test/whot/internal/conf"
)

type Log struct {
	tableID int32
	sink    *filelog.Log
}

func tableLogPath(tableID int32) string {
	root := strings.TrimSpace(os.Getenv("YOLA_TEST_LOG_DIR"))
	if root == "" {
		root = "logs"
	}
	return filepath.Join(root, "log_cache", conf.Name, fmt.Sprintf("table_%d.log", tableID))
}

func newTableLog(tableID int32, c *conf.Room_LogCache) *Log {
	logger := &Log{tableID: tableID}
	if c == nil || !c.Open {
		return logger
	}
	path := tableLogPath(tableID)
	sink, err := filelog.New(path)
	if err != nil {
		slog.Error("disable table file log", "path", path, "error", err)
		return logger
	}
	logger.sink = sink
	return logger
}

func (l *Log) Close() error {
	if !l.enabled() {
		return nil
	}
	return l.sink.Close()
}

func (l *Log) write(message string, args ...any) {
	if !l.enabled() {
		return
	}
	if err := l.sink.Write(message, args...); err != nil {
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

func (l *Log) userExit(p *player.Player, seatCount int32, chairID int32, switchTable bool) {
	l.writePlayer(p, "[离开房间] 玩家:%+v 桌子人数(%+v) lastChair(%d) 是否换桌(%+v) ", seatCount, chairID, switchTable)
}

func (l *Log) offline(p *player.Player) {
	l.writePlayer(p, "【玩家断线】玩家:%+v ")
}

func (l *Log) begin(table string, bet float64, seats []*player.Player, info any) {
	if !l.enabled() {
		return
	}
	logs := []string{fmt.Sprintf("[游戏开始] %s bet:%.1f Gamer=%v", table, bet, info)}
	for _, p := range seats {
		if p != nil {
			logs = append(logs, fmt.Sprintf("玩家:%+v 投注[%+v] Hands:%v 状态:%v", p.Desc(), bet, p.GetCards(), p.GetStatus()))
		}
	}
	l.write("%s", strings.Join(logs, "\r\n"))
}

func (l *Log) activePush(p *player.Player, currentCard int32, pending *v1.Pending, allowed []*v1.ActionOption) {
	if !l.enabled() {
		return
	}
	l.writePlayer(p, "[操作通知] 玩家:%+v curr=%d, pending=%v, canOp=%v", currentCard, descPendingEffect(pending), xgo.ToJSON(allowed))
}

func (l *Log) stage(stage string, active int32) {
	l.write("[状态转移] %s. active=%+v", stage, active)
}

func (l *Log) play(p *player.Player, card int32, pending *v1.Pending, timeout bool) {
	if !l.enabled() {
		return
	}
	l.writePlayer(p, "[玩家出牌] 玩家:%+v. out=[%+v] pending=%s, timeout=%+v", card, descPending(pending), timeout)
}

func (l *Log) draw(p *player.Player, cards []int32, pending *v1.Pending, timeout bool) {
	if !l.enabled() {
		return
	}
	l.writePlayer(p, "[玩家抓牌] 玩家:%+v. drawn=%+v, pending=%s, timeout=%+v", cards, descPending(pending), timeout)
}

func (l *Log) replyPending(p *player.Player, action v1.ACTION, pending *v1.Pending) {
	if !l.enabled() {
		return
	}
	l.writePlayer(p, "[响应Pending] 玩家:%+v. action=%q 响应了pending，清除:%s", action, descPending(pending))
}

func (l *Log) market(p *player.Player, cards []int32, pending *v1.Pending, timeout bool) {
	if !l.enabled() {
		return
	}
	l.writePlayer(p, "[market各抽一张] 玩家:%+v. drawn=%+v, pending=%s, timeout=%+v", cards, descPending(pending), timeout)
}

func (l *Log) skipTurn(p *player.Player, timeout bool) {
	l.writePlayer(p, "[suspend玩家跳过] 玩家:%+v pending=, timeout=%v", timeout)
}

func (l *Log) declareSuit(p *player.Player, suit v1.SUIT, currentCard int32, timeout bool) {
	l.writePlayer(p, "[Whot确定花色] 玩家:%+v 花色=%d, currCard=%v, timeout=%+v", suit, currentCard, timeout)
}

func descPending(pending *v1.Pending) string {
	if pending == nil {
		return ""
	}
	return fmt.Sprintf("{%+v->%v %v %v} ", pending.Initiator, pending.Target, pending.Effect, pending.Quantity)
}

func descPendingEffect(pending *v1.Pending) string {
	if pending == nil {
		return ""
	}
	return fmt.Sprintf("%q", pending.Effect)
}

func logPlayers(players []*player.Player) string {
	logs := []string{""}
	for _, p := range players {
		if p != nil {
			logs = append(logs, fmt.Sprintf("<玩家>:%+v Hands:%v 状态:%v", p.Desc(), p.GetCards(), p.GetStatus()))
		}
	}
	return strings.Join(logs, "\r\n")
}

func (l *Log) settle(winner *player.Player, win, tax float64, messages ...any) {
	if !l.enabled() {
		return
	}
	logs := []string{"[结算]"}
	if winner != nil {
		logs = append(logs, fmt.Sprintf("<赢家>:%+v win:%.1f tax:%.1f Hands:%v", winner.Desc(), win, tax, winner.GetCards()))
	}
	for _, message := range messages {
		logs = append(logs, fmt.Sprint(message))
	}
	l.write("%s", strings.Join(logs, "\r\n"))
}

func (l *Log) end(messages ...any) {
	l.write("[GameEnd] %s", messages)
	l.write("\r\n\r\n\r\n")
}
