package table

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"yola/test/ludo/api/v1"
	"yola/test/ludo/internal/biz/player"
	"yola/test/ludo/internal/conf"
	"yola/test/ludo/internal/model"
	"yola/test/ludo/pkg/codes"
)

type tableRepoStub struct {
	logoutCount int
}

func (repo *tableRepoStub) LogoutGame(*player.Player, int32, string) error {
	repo.logoutCount++
	return nil
}

func TestMoveTimeoutAdvancesTurn(t *testing.T) {
	room := &conf.Room{
		Table:    &conf.Room_Table{TableNum: 1, ChairNum: 2},
		Game:     &conf.Room_Game{MinMoney: 100, MaxMoney: 1000, BaseMoney: 100},
		Robot:    &conf.Room_Robot{},
		LogCache: &conf.Room_LogCache{},
	}
	repo := new(tableRepoStub)
	gameTable := newStartedManager(t, room, repo).table(1)
	p := player.New(&player.Raw{ID: 42, BaseData: &player.BaseData{UID: 42, Money: 500}})
	p.Reset()
	p.SetTableID(1)
	p.SetChairID(0)
	p.SetGaming()
	p.SetColor(0)
	gameTable.seats[0] = p
	gameTable.seatCount.Store(1)
	gameTable.activeChair = 0
	gameTable.board = model.NewBoard([]int32{0}, 4, true)
	p.SetPieces(gameTable.board.GetPieceIDsByColor(0))
	p.AddDice(1)
	gameTable.stage.Set(StMove, time.Second, 1)

	gameTable.onMoveTimeout()

	if p.HasUnusedDice(1) {
		t.Fatal("move timeout left the valid dice unused")
	}
	if gameTable.stage.State() == StMove {
		t.Fatal("move timeout left table stuck in StMove")
	}
}

func TestGameEndReclaimsOfflinePlayer(t *testing.T) {
	room := &conf.Room{
		Table:    &conf.Room_Table{TableNum: 1, ChairNum: 2},
		Game:     &conf.Room_Game{MinMoney: 100, MaxMoney: 1000, BaseMoney: 100},
		Robot:    &conf.Room_Robot{},
		LogCache: &conf.Room_LogCache{},
	}
	repo := new(tableRepoStub)
	tables := newStartedManager(t, room, repo)
	p := player.New(&player.Raw{ID: 42, BaseData: &player.BaseData{UID: 42, Money: 500}})
	if err := tables.call(context.Background(), 1, func(gameTable *Table) error {
		p.Reset()
		p.SetTableID(1)
		p.SetChairID(0)
		p.SetGaming()
		p.SetOffline(true)
		gameTable.seats[0] = p
		gameTable.seatCount.Store(1)
		gameTable.board = model.NewBoard([]int32{0}, 4, true)
		gameTable.stage.Set(StResult, time.Second, 1)
		gameTable.resetForNextGame()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := tables.call(context.Background(), 1, func(gameTable *Table) error {
		if repo.logoutCount != 1 || gameTable.playerAt(0) != nil {
			return fmt.Errorf("offline player after game end: logout=%d seated=%v", repo.logoutCount, gameTable.playerAt(0) != nil)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRoundRunsDiceMoveResultAndStartsNextRound(t *testing.T) {
	room := &conf.Room{
		Table:    &conf.Room_Table{TableNum: 1, ChairNum: 2},
		Game:     &conf.Room_Game{MinMoney: 100, MaxMoney: 1000, BaseMoney: 100},
		Robot:    &conf.Room_Robot{},
		LogCache: &conf.Room_LogCache{},
	}
	tables := newStartedManager(t, room, new(tableRepoStub))
	useManualTimers(t, tables)
	players := []*player.Player{
		player.New(&player.Raw{ID: 1, BaseData: &player.BaseData{UID: 1, Money: 500}}),
		player.New(&player.Raw{ID: 2, BaseData: &player.BaseData{UID: 2, Money: 500}}),
	}
	if err := tables.call(context.Background(), 1, func(gameTable *Table) error {
		for _, p := range players {
			if !seatForTest(gameTable, p) || !gameTable.Ready(p, true) {
				return fmt.Errorf("ready player %d", p.GetPlayerID())
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertStage := func(want StageID) {
		t.Helper()
		if err := tables.call(context.Background(), 1, func(gameTable *Table) error {
			if state := gameTable.stage.State(); state != want {
				return fmt.Errorf("stage = %s, want %s", state, want)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	fireStage := func() {
		t.Helper()
		var timerID int64
		if err := tables.call(context.Background(), 1, func(gameTable *Table) error {
			timerID = gameTable.stage.TimerID()
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if !fireTableTimer(t, tables, timerID) {
			t.Fatalf("stage timer %d is missing", timerID)
		}
		if err := tables.call(context.Background(), 1, func(*Table) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}

	assertStage(StReady)
	fireStage()
	assertStage(StSendCard)
	for _, p := range players {
		if money := p.GetBaseData().Money; money != 400 {
			t.Fatalf("player %d money = %.0f, want 400", p.GetPlayerID(), money)
		}
	}
	fireStage()
	assertStage(StDice)
	var fastTimerID int64
	if err := tables.call(context.Background(), 1, func(gameTable *Table) error {
		active := gameTable.activePlayer()
		if active == nil {
			return fmt.Errorf("active player is missing")
		}
		diceRsp := gameTable.RollDice(active, false)
		if diceRsp == nil {
			return fmt.Errorf("dice request failed")
		}
		moveDice := diceRsp.Dice
		if gameTable.stage.State() != StMove {
			active = gameTable.activePlayer()
			moveDice = 1
			active.AddDice(moveDice)
			gameTable.allowPlayerToMove(active)
		}
		moves := gameTable.board.CalcAllMovable(active.GetColor(), []int32{moveDice})
		if len(moves) == 0 {
			return fmt.Errorf("active player has no move for dice %d", moveDice)
		}
		moveRsp := gameTable.MovePiece(active, &v1.MoveReq{
			UserId:    active.GetPlayerID(),
			PieceId:   moves[0][0],
			DiceValue: moveDice,
		}, false)
		if moveRsp == nil {
			return fmt.Errorf("move request failed")
		}
		fastTimerID = gameTable.fastTimerID
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !fireTableTimer(t, tables, fastTimerID) {
		t.Fatalf("fast result timer %d is missing", fastTimerID)
	}
	if err := tables.call(context.Background(), 1, func(*Table) error { return nil }); err != nil {
		t.Fatal(err)
	}
	assertStage(StResult)
	for _, p := range players {
		if money := p.GetAllMoney(); money != 500 {
			t.Fatalf("timeout refund for player %d = %.0f, want 500", p.GetPlayerID(), money)
		}
	}
	fireStage()
	assertStage(StWait)
	if err := tables.call(context.Background(), 1, func(gameTable *Table) error {
		for _, p := range players {
			if !gameTable.Ready(p, true) {
				return fmt.Errorf("ready player %d for next round", p.GetPlayerID())
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertStage(StReady)
	fireStage()
	assertStage(StSendCard)
}

func TestSettleGamePaysWinnerAndBuildsResult(t *testing.T) {
	room := &conf.Room{
		Table:    &conf.Room_Table{TableNum: 1, ChairNum: 2},
		Game:     &conf.Room_Game{MinMoney: 100, MaxMoney: 1000, BaseMoney: 100, Fee: 0.2},
		Robot:    &conf.Room_Robot{},
		LogCache: &conf.Room_LogCache{},
	}
	tables := newStartedManager(t, room, new(tableRepoStub))
	players := []*player.Player{
		player.New(&player.Raw{ID: 1, BaseData: &player.BaseData{UID: 1, Money: 500}}),
		player.New(&player.Raw{ID: 2, BaseData: &player.BaseData{UID: 2, Money: 500}}),
	}

	if err := tables.call(context.Background(), 1, func(gameTable *Table) error {
		for _, p := range players {
			if !seatForTest(gameTable, p) {
				return fmt.Errorf("seat player %d", p.GetPlayerID())
			}
			p.SetGaming()
			p.StartGame(room.Game.BaseMoney)
		}
		result := gameTable.settleGame(players[0], v1.FINISH_TYPE_PLAYER_HAND_EMPTY)
		if result.FinishType != v1.FINISH_TYPE_PLAYER_HAND_EMPTY || result.WinnerID != 1 || len(result.Results) != 2 {
			return fmt.Errorf("result = %+v", result)
		}
		loser, winner := result.Results[0], result.Results[1]
		if loser.UserID != 2 || loser.WinScore != -100 ||
			winner.UserID != 1 || !winner.IsWinner || winner.WinScore != 180 {
			return fmt.Errorf("player results = %+v", result.Results)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if players[0].GetAllMoney() != 580 || players[1].GetAllMoney() != 400 {
		t.Fatalf("money after settlement = (%.0f, %.0f), want (580, 400)", players[0].GetAllMoney(), players[1].GetAllMoney())
	}
}

func TestResultPushSchedulesRobotExit(t *testing.T) {
	room := &conf.Room{
		Table:    &conf.Room_Table{TableNum: 1, ChairNum: 1},
		Game:     &conf.Room_Game{MinMoney: 100, MaxMoney: 1000, BaseMoney: 100},
		Robot:    &conf.Room_Robot{Open: true, TableMaxCount: 1},
		LogCache: &conf.Room_LogCache{},
	}
	repo := new(tableRepoStub)
	tables := newStartedManager(t, room, repo)
	useManualTimers(t, tables)
	robot := player.New(&player.Raw{
		ID:       1,
		IsRobot:  true,
		BaseData: &player.BaseData{UID: 1, Money: 500},
	})
	var exitTimerID int64
	if err := tables.call(context.Background(), 1, func(gameTable *Table) error {
		if !seatForTest(gameTable, robot) {
			return errors.New("seat robot")
		}
		robot.SetGaming()
		robot.StartGame(room.Game.BaseMoney)
		gameTable.settleGame(robot, v1.FINISH_TYPE_PLAYER_HAND_EMPTY)
		if total := testTimers(t, tables).Monitor().Total; total != 2 {
			return fmt.Errorf("scheduled timers = %d, want result and robot exit timers", total)
		}
		exitTimerID = latestTableTimerID(tables)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !fireTableTimer(t, tables, exitTimerID) {
		t.Fatalf("robot exit timer %d is missing", exitTimerID)
	}
	if err := tables.call(context.Background(), 1, func(gameTable *Table) error {
		if gameTable.playerAt(0) != nil {
			return errors.New("robot remained seated after result")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if repo.logoutCount != 1 {
		t.Fatalf("robot logout count = %d, want 1", repo.logoutCount)
	}
}

func TestRobotReservedTablesRoundUpMinPlayCount(t *testing.T) {
	room := &conf.Room{
		Table:    &conf.Room_Table{TableNum: 3, ChairNum: 4},
		Game:     &conf.Room_Game{MinMoney: 100, MaxMoney: 1000, BaseMoney: 100},
		Robot:    &conf.Room_Robot{Open: true, TableMaxCount: 4, MinPlayCount: 5},
		LogCache: &conf.Room_LogCache{},
	}
	tables := newStartedManager(t, room, new(tableRepoStub))
	robot := player.New(&player.Raw{
		ID:       1,
		IsRobot:  true,
		BaseData: &player.BaseData{UID: 1, Money: 500},
	})
	if !tables.table(2).robotLogic.canEnter(robot) {
		t.Fatal("second table was not reserved for the fifth robot")
	}
	if tables.table(3).robotLogic.canEnter(robot) {
		t.Fatal("third table was reserved beyond min_play_count")
	}
}

func TestRobotTimerIgnoresPlayerAfterTableExit(t *testing.T) {
	room := &conf.Room{
		Table:    &conf.Room_Table{TableNum: 1, ChairNum: 1},
		Game:     &conf.Room_Game{MinMoney: 100, MaxMoney: 1000, BaseMoney: 100},
		Robot:    &conf.Room_Robot{Open: true, TableMaxCount: 1},
		LogCache: &conf.Room_LogCache{},
	}
	repo := new(tableRepoStub)
	tables := newStartedManager(t, room, repo)
	useManualTimers(t, tables)
	robot := player.New(&player.Raw{
		ID:       1,
		IsRobot:  true,
		BaseData: &player.BaseData{UID: 1, Money: 500},
	})
	var timerID int64
	if err := tables.call(context.Background(), 1, func(gameTable *Table) error {
		if !seatForTest(gameTable, robot) {
			return fmt.Errorf("seat robot")
		}
		robot.SetGaming()
		gameTable.activeChair = robot.GetChairID()
		gameTable.stage.Set(StDice, time.Second, 0)
		gameTable.robotLogic.onActivePlayer(robot, &v1.ActivePush{Active: robot.GetChairID()})
		timerID = latestTableTimerID(tables)
		robot.Reset()
		gameTable.stage.Set(StWait, time.Second, 0)
		exited, exitErr := gameTable.Exit(robot, codes.Success, "logout")
		if exitErr != nil || !exited {
			return fmt.Errorf("exit robot")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !fireTableTimer(t, tables, timerID) {
		t.Fatalf("robot timer %d is missing", timerID)
	}
	if err := tables.call(context.Background(), 1, func(gameTable *Table) error {
		if gameTable.playerAt(0) != nil || robot.GetTableID() != player.TableIDDetached {
			return fmt.Errorf("robot timer changed exited player route")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if repo.logoutCount != 1 {
		t.Fatalf("player exit events = %d, want 1", repo.logoutCount)
	}
}

func TestRobotTimerIgnoresStaleStageToken(t *testing.T) {
	room := &conf.Room{
		Table:    &conf.Room_Table{TableNum: 1, ChairNum: 1},
		Game:     &conf.Room_Game{MinMoney: 100, MaxMoney: 1000, BaseMoney: 100},
		Robot:    &conf.Room_Robot{Open: true, TableMaxCount: 1},
		LogCache: &conf.Room_LogCache{},
	}
	tables := newStartedManager(t, room, new(tableRepoStub))
	useManualTimers(t, tables)
	robot := player.New(&player.Raw{
		ID:       1,
		IsRobot:  true,
		BaseData: &player.BaseData{UID: 1, Money: 500},
	})
	var timerID int64
	if err := tables.call(context.Background(), 1, func(gameTable *Table) error {
		if !seatForTest(gameTable, robot) {
			return fmt.Errorf("seat robot")
		}
		robot.SetGaming()
		robot.SetColor(0)
		gameTable.activeChair = robot.GetChairID()
		gameTable.board = model.NewBoard([]int32{0}, 4, true)
		robot.SetPieces(gameTable.board.GetPieceIDsByColor(0))
		gameTable.stage.Set(StDice, time.Second, 11)
		gameTable.robotLogic.onActivePlayer(robot, &v1.ActivePush{Active: robot.GetChairID()})
		timerID = latestTableTimerID(tables)
		gameTable.stage.Set(StDice, time.Second, 12)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !fireTableTimer(t, tables, timerID) {
		t.Fatalf("robot timer %d is missing", timerID)
	}
	if err := tables.call(context.Background(), 1, func(gameTable *Table) error {
		if dices := robot.DiceListInt32(); len(dices) != 0 {
			return fmt.Errorf("stale robot timer rolled dice %v", dices)
		}
		if timerID := gameTable.stage.TimerID(); timerID != 12 {
			return fmt.Errorf("stage timer ID = %d, want 12", timerID)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func seatForTest(gameTable *Table, p *player.Player) bool {
	seated, err := gameTable.Seat(p)
	return err == nil && seated
}

var _ Repo = (*tableRepoStub)(nil)
