package biz

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"yola/test/internal/xgo"
	"yola/test/ludo/api/v1"
	"yola/test/ludo/internal/biz/player"
	"yola/test/ludo/internal/biz/table"
	"yola/test/ludo/internal/conf"
	"yola/test/ludo/internal/pressure"
	"yola/test/ludo/pkg/codes"
)

const (
	playerCleanupTimeout          = 2 * time.Second
	playerCleanupOperationTimeout = 500 * time.Millisecond
	playerCleanupWorkers          = 16
)

func (uc *Usecase) SetClientPusher(pusher table.ClientPusher) {
	uc.tm.SetClientPusher(pusher)
}

func (uc *Usecase) Disconnect(ctx context.Context, sess player.Session) error {
	if sess == nil {
		return nil
	}
	uid, err := strconv.ParseInt(sess.UID(), 10, 64)
	if err != nil {
		return nil
	}
	p := uc.pm.GetByID(uid)
	if p == nil || p.GetTableID() == player.TableIDDetached {
		return nil
	}
	return uc.tm.CallPlayer(ctx, p, func(gameTable *table.Table) error {
		current := p.GetSession()
		if current == nil || current.BindingToken() != sess.BindingToken() {
			return nil
		}
		return gameTable.Offline(p)
	})
}

func (uc *Usecase) Login(ctx context.Context, sess player.Session, uid int64, token string) (*v1.LoginRsp, error) {
	current := uc.pm.GetByID(uid)
	if current == nil {
		return uc.enterRoom(ctx, sess, uid, token)
	}
	if token == "" {
		return loginResponse(current, codes.TokenFail, "TOKEN_FAIL"), nil
	}
	if current.GetTableID() > 0 {
		response, err := uc.reconnect(ctx, sess, current, token)
		if err == nil || (!errors.Is(err, table.ErrTableNotFound) && !errors.Is(err, table.ErrPlayerRouteChanged)) {
			return response, err
		}
	}
	if current.GetTableID() == player.TableIDPending || !current.TryBeginExit() {
		return loginResponse(current, codes.PlayerAlreadyInTable, "PLAYER_ALREADY_IN_TABLE"), nil
	}
	if err := uc.LogoutGame(current, codes.TableNotFound, "TABLE_NOT_FOUND"); err != nil {
		return nil, fmt.Errorf("recover detached player %d: %w", uid, err)
	}
	return uc.enterRoom(ctx, sess, uid, token)
}

func (uc *Usecase) Logout(ctx context.Context, uid int64) (*v1.LogoutRsp, error) {
	p, err := uc.managedPlayer(uid)
	if err != nil {
		return nil, err
	}
	exited := false
	err = uc.callPlayer(ctx, p, func(gameTable *table.Table) error {
		var exitErr error
		exited, exitErr = gameTable.Exit(p, codes.Success, "success")
		return exitErr
	})
	if err != nil {
		return nil, fmt.Errorf("logout player %d: %w", uid, err)
	}
	if !exited {
		return &v1.LogoutRsp{Code: codes.ExitTableFail, Msg: "EXIT_TABLE_FAIL", UserID: uid}, nil
	}
	return &v1.LogoutRsp{UserID: uid}, nil
}

func (uc *Usecase) Ready(ctx context.Context, uid int64, ready bool) (*v1.ReadyRsp, error) {
	p, err := uc.managedPlayer(uid)
	if err != nil {
		return nil, err
	}
	var reply *v1.ReadyRsp
	err = uc.callPlayer(ctx, p, func(gameTable *table.Table) error {
		if !gameTable.Ready(p, ready) {
			return ErrReadyRejected
		}
		reply = &v1.ReadyRsp{UserID: uid, IsReady: ready}
		gameTable.BroadcastReadyPush(p, reply)
		return nil
	})
	return reply, err
}

func (uc *Usecase) SwitchTable(ctx context.Context, uid int64) (*v1.SwitchTableRsp, error) {
	p, err := uc.managedPlayer(uid)
	if err != nil {
		return nil, err
	}
	code, msg, err := uc.tm.Switch(ctx, p, uc.rc.Game)
	if err != nil {
		if p.GetTableID() == player.TableIDDetached && uc.pm.GetByID(uid) == p && p.TryBeginExit() {
			err = errors.Join(err, uc.LogoutGame(p, codes.ExitTableFail, "SWITCH_TABLE_RECOVERY_FAILED"))
		}
		return nil, fmt.Errorf("switch player %d table: %w", uid, mapTableCallError(err))
	}
	return &v1.SwitchTableRsp{Code: code, Msg: msg, UserID: uid}, nil
}

func (uc *Usecase) Scene(ctx context.Context, uid int64) (*v1.SceneRsp, error) {
	p, err := uc.managedPlayer(uid)
	if err != nil {
		return nil, err
	}
	var reply *v1.SceneRsp
	err = uc.callPlayer(ctx, p, func(gameTable *table.Table) error {
		reply = gameTable.Scene()
		return nil
	})
	return reply, err
}

func (uc *Usecase) Chat(ctx context.Context, uid int64, req *v1.ChatReq) (*v1.ChatRsp, error) {
	p, err := uc.managedPlayer(uid)
	if err != nil {
		return nil, err
	}
	var reply *v1.ChatRsp
	err = uc.callPlayer(ctx, p, func(gameTable *table.Table) error {
		reply = &v1.ChatRsp{UserID: req.UserID, OpType: req.OpType, FaceID: req.FaceID, Msg: req.Msg}
		gameTable.BroadcastChatPush(p, reply)
		return nil
	})
	return reply, err
}

func (uc *Usecase) Hosting(ctx context.Context, uid int64, hosting bool) (*v1.HostingRsp, error) {
	p, err := uc.managedPlayer(uid)
	if err != nil {
		return nil, err
	}
	var reply *v1.HostingRsp
	err = uc.callPlayer(ctx, p, func(gameTable *table.Table) error {
		if !gameTable.SetHosting(p, hosting) {
			return ErrHostingRejected
		}
		status := int32(0)
		if hosting {
			status = 1
		}
		reply = &v1.HostingRsp{ChairID: p.GetChairID(), Status: status, AiNum: p.GetTimeoutCnt()}
		gameTable.BroadcastHostingPush(p, reply)
		return nil
	})
	return reply, err
}

func (uc *Usecase) Forward(ctx context.Context, uid int64, req *v1.ForwardReq) (*v1.ForwardRsp, error) {
	p, err := uc.managedPlayer(uid)
	if err != nil {
		return nil, err
	}
	var reply *v1.ForwardRsp
	err = uc.callPlayer(ctx, p, func(gameTable *table.Table) error {
		reply = &v1.ForwardRsp{Type: req.Type, Msg: req.Msg}
		gameTable.BroadcastForwardPush(p, reply)
		return nil
	})
	return reply, err
}

func (uc *Usecase) Dice(ctx context.Context, uid int64) (*v1.DiceRsp, error) {
	p, err := uc.managedPlayer(uid)
	if err != nil {
		return nil, err
	}
	var reply *v1.DiceRsp
	err = uc.callPlayer(ctx, p, func(gameTable *table.Table) error {
		reply = gameTable.RollDice(p, false)
		if reply == nil {
			reply = &v1.DiceRsp{Code: codes.Fail, Msg: "DICE_NOT_ALLOWED", Uid: uid}
			return nil
		}
		gameTable.BroadcastDicePush(p, reply)
		return nil
	})
	return reply, err
}

func (uc *Usecase) Move(ctx context.Context, uid int64, req *v1.MoveReq) (*v1.MoveRsp, error) {
	p, err := uc.managedPlayer(uid)
	if err != nil {
		return nil, err
	}
	var reply *v1.MoveRsp
	err = uc.callPlayer(ctx, p, func(gameTable *table.Table) error {
		reply = gameTable.MovePiece(p, req, false)
		if reply == nil {
			reply = &v1.MoveRsp{Code: int64(codes.Fail), Msg: "MOVE_NOT_ALLOWED"}
			return nil
		}
		gameTable.BroadcastMovePush(p, reply)
		return nil
	})
	return reply, err
}

func (uc *Usecase) callPlayer(ctx context.Context, p *player.Player, action func(*table.Table) error) error {
	return mapTableCallError(uc.tm.CallPlayer(ctx, p, action))
}

func (uc *Usecase) managedPlayer(uid int64) (*player.Player, error) {
	p := uc.pm.GetByID(uid)
	if p == nil {
		return nil, fmt.Errorf("%w: player %d not found", ErrInvalidPlayerState, uid)
	}
	return p, nil
}

func mapTableCallError(err error) error {
	if errors.Is(err, table.ErrTableNotFound) || errors.Is(err, table.ErrPlayerRouteChanged) || errors.Is(err, table.ErrPlayerExitInProgress) {
		return fmt.Errorf("%w: %v", ErrInvalidPlayerState, err)
	}
	return err
}

func (uc *Usecase) reconnect(ctx context.Context, sess player.Session, p *player.Player, token string) (*v1.LoginRsp, error) {
	if token == "" {
		return loginResponse(p, codes.TokenFail, "TOKEN_FAIL"), nil
	}
	if err := sess.BindNode(ctx); err != nil {
		return nil, fmt.Errorf("bind reconnecting player %d: %w", p.GetPlayerID(), err)
	}
	lifecycleCtx, cancelLifecycle := context.WithTimeout(context.Background(), playerCleanupTimeout)
	err := uc.tm.CallPlayer(lifecycleCtx, p, func(gameTable *table.Table) error {
		previousSession := p.GetSession()
		p.UpdateSession(sess)
		if err := gameTable.ReEnter(p); err != nil {
			if p.GetTableID() > 0 {
				p.UpdateSession(previousSession)
			}
			return err
		}
		return nil
	})
	if err != nil {
		var unbindErr error
		if uc.pm.GetByID(p.GetPlayerID()) == p && p.GetTableID() != player.TableIDDetached {
			unbindErr = sess.UnbindNode(lifecycleCtx)
		}
		cancelLifecycle()
		return nil, errors.Join(fmt.Errorf("re-enter player %d: %w", p.GetPlayerID(), err), unbindErr)
	}
	cancelLifecycle()
	return loginResponse(p, codes.Success, "ReEnter"), nil
}

func (uc *Usecase) enterRoom(ctx context.Context, sess player.Session, uid int64, token string) (*v1.LoginRsp, error) {
	raw := &player.Raw{
		ID:      uid,
		Session: sess,
	}
	p, err := uc.createPlayer(ctx, raw)
	if err != nil {
		slog.Error("create player", "uid", uid, "error", err)
		return &v1.LoginRsp{
			Code:    codes.CreatePlayerFail,
			Msg:     "CREATE_PLAYER_FAIL",
			UserID:  uid,
			ArenaID: int32(conf.ArenaID),
		}, nil
	}
	if token == "" {
		p.UpdateSession(nil)
		return loginResponse(p, codes.TokenFail, "TOKEN_FAIL"), nil
	}
	if err = applyPressureMoney(p, token, uc.rc.Game); err != nil {
		p.UpdateSession(nil)
		return loginResponse(p, codes.TokenFail, "TOKEN_FAIL"), nil
	}
	if code, msg := table.CheckRoomLimit(p, uc.rc.Game); code != codes.Success {
		slog.Error("room entry rejected", "uid", uid, "code", code, "message", msg)
		p.UpdateSession(nil)
		return loginResponse(p, code, msg), nil
	}
	current, added := uc.pm.AddIfAbsent(p)
	if !added {
		p.UpdateSession(nil)
		return loginResponse(current, codes.PlayerAlreadyInTable, "PLAYER_ALREADY_IN_TABLE"), nil
	}
	if err = sess.BindNode(ctx); err != nil {
		uc.pm.RemoveIfSame(p)
		p.UpdateSession(nil)
		return nil, fmt.Errorf("bind player %d: %w", uid, err)
	}

	lifecycleCtx, cancelLifecycle := context.WithTimeout(context.Background(), playerCleanupTimeout)
	defer cancelLifecycle()
	code, msg, enterErr := uc.tm.Enter(lifecycleCtx, p)
	if enterErr == nil && code == codes.Success {
		return loginResponse(p, code, msg), nil
	}

	managed := uc.pm.GetByID(uid) == p
	retainForCleanup := managed && p.GetTableID() == player.TableIDDetached
	var unbindErr error
	if managed && !retainForCleanup {
		unbindErr = sess.UnbindNode(lifecycleCtx)
		uc.pm.RemoveIfSame(p)
	}
	if !retainForCleanup {
		p.UpdateSession(nil)
	}
	if enterErr != nil {
		return nil, errors.Join(
			fmt.Errorf("enter player %d: %w", uid, enterErr),
			unbindErr,
		)
	}
	if unbindErr != nil {
		return nil, errors.Join(
			fmt.Errorf("enter room failed: code=%d message=%s", code, msg),
			fmt.Errorf("rollback node binding for player %d: %w", uid, unbindErr),
		)
	}
	slog.Error("enter table", "uid", uid, "code", code, "message", msg)
	return loginResponse(p, code, msg), nil
}

func applyPressureMoney(p *player.Player, token string, game *conf.Room_Game) error {
	money, pressureLogin, err := pressure.ParseToken(token)
	if err != nil || !pressureLogin {
		return err
	}
	if p == nil || p.GetBaseData() == nil || game == nil {
		return errors.New("pressure player money dependencies are missing")
	}
	if money.Min < max(game.MinMoney, game.BaseMoney) || game.MaxMoney != -1 && money.Max > game.MaxMoney {
		return fmt.Errorf("pressure money range [%v,%v] exceeds room range", money.Min, money.Max)
	}
	value := money.Min
	if money.Max > money.Min {
		value = xgo.RandFloat(money.Min, money.Max)
	}
	p.GetBaseData().Money = value
	return nil
}

func loginResponse(p *player.Player, code int32, msg string) *v1.LoginRsp {
	response := &v1.LoginRsp{
		Code:    code,
		Msg:     msg,
		ArenaID: int32(conf.ArenaID),
	}
	if p != nil {
		response.UserID = p.GetPlayerID()
		response.TableID = p.GetTableID()
		response.ChairID = p.GetChairID()
	}
	return response
}

func (uc *Usecase) CreateRobot(raw *player.Raw) (*player.Player, error) {
	base := &player.BaseData{
		UID:       raw.ID,
		VIP:       0,
		NickName:  fmt.Sprintf("robot_%d", raw.ID),
		Avatar:    fmt.Sprintf("avatar_%d", raw.ID),
		AvatarURL: fmt.Sprintf("avatar_%d", raw.ID),
		Money:     xgo.RandFloat(uc.rc.Game.MinMoney, uc.rc.Game.MaxMoney),
	}
	raw.BaseData = base
	return player.New(raw), nil
}

func (uc *Usecase) EnterRobots(ctx context.Context, players []*player.Player) ([]int64, error) {
	return uc.tm.EnterRobots(ctx, players)
}

func (uc *Usecase) createPlayer(ctx context.Context, raw *player.Raw) (*player.Player, error) {
	base, err := uc.repo.LoadPlayer(ctx, raw.ID)
	switch {
	case errors.Is(err, ErrPlayerNotFound):
		base = &player.BaseData{
			UID:       raw.ID,
			VIP:       0,
			NickName:  fmt.Sprintf("user_%d", raw.ID),
			Avatar:    fmt.Sprintf("avatar_%d", raw.ID%15),
			AvatarURL: fmt.Sprintf("avatar_%d", raw.ID%15),
			Money:     float64(int64(xgo.RandFloat(uc.rc.Game.MinMoney, uc.rc.Game.MaxMoney))),
		}
	case err != nil:
		return nil, fmt.Errorf("load player %d: %w", raw.ID, err)
	case base == nil:
		return nil, fmt.Errorf("load player %d: empty result", raw.ID)
	}
	raw.BaseData = base
	p := player.New(raw)
	if slog.Default().Enabled(context.Background(), slog.LevelDebug) {
		slog.Debug("create player", "player", p.Desc())
	}
	return p, nil
}

func (uc *Usecase) LogoutGame(p *player.Player, code int32, message string) error {
	if p == nil {
		return nil
	}
	uid := p.GetPlayerID()
	if p.IsRobot() {
		if uc.rm != nil {
			uc.rm.Leave(uid)
		}
		p.CancelExit()
		return nil
	}
	cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), playerCleanupTimeout)
	err := uc.persistAndUnbindPlayer(cleanupCtx, p)
	cancelCleanup()
	if err != nil {
		p.CancelExit()
		return err
	}
	uc.pm.RemoveIfSame(p)
	slog.Info("logout player", "uid", uid, "code", code, "message", message)
	return nil
}

func (uc *Usecase) closePlayers(ctx context.Context) error {
	if ctx == nil {
		return errors.New("close Ludo players: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("close Ludo players: %w", err)
	}
	players := uc.pm.All()
	if len(players) == 0 {
		return nil
	}
	type cleanupResult struct {
		player *player.Player
		err    error
	}
	jobs := make(chan *player.Player, len(players))
	results := make(chan cleanupResult, len(players))
	for _, p := range players {
		jobs <- p
	}
	close(jobs)
	workerCount := min(playerCleanupWorkers, len(players))
	for range workerCount {
		go func() {
			for p := range jobs {
				results <- cleanupResult{player: p, err: uc.persistAndUnbindPlayer(ctx, p)}
			}
		}()
	}
	cleanupErrors := make([]error, 0, len(players))
	for range players {
		select {
		case result := <-results:
			if result.err != nil {
				cleanupErrors = append(cleanupErrors, result.err)
			} else {
				uc.pm.RemoveIfSame(result.player)
			}
		case <-ctx.Done():
			cleanupErrors = append(cleanupErrors, fmt.Errorf("wait for Ludo player cleanup: %w", ctx.Err()))
			return errors.Join(cleanupErrors...)
		}
	}
	return errors.Join(cleanupErrors...)
}

func (uc *Usecase) persistAndUnbindPlayer(ctx context.Context, p *player.Player) error {
	uid := p.GetPlayerID()
	if ctx == nil {
		return fmt.Errorf("cleanup player %d: context is nil", uid)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("cleanup player %d: %w", uid, err)
	}
	var saveErr error
	if base := p.GetBaseData(); base == nil {
		saveErr = fmt.Errorf("save player %d: base data is missing", uid)
	} else {
		baseData := *base
		saveCtx, cancelSave := context.WithTimeout(ctx, playerCleanupOperationTimeout)
		err := uc.repo.SavePlayer(saveCtx, &baseData)
		cancelSave()
		if err != nil {
			saveErr = fmt.Errorf("save player %d: %w", uid, err)
		}
	}

	var unbindErr error
	if sess := p.GetSession(); sess != nil {
		if err := ctx.Err(); err != nil {
			unbindErr = fmt.Errorf("unbind player %d: %w", uid, err)
		} else {
			unbindCtx, cancelUnbind := context.WithTimeout(ctx, playerCleanupOperationTimeout)
			err := sess.UnbindNode(unbindCtx)
			cancelUnbind()
			if err != nil {
				unbindErr = fmt.Errorf("unbind player %d: %w", uid, err)
			}
		}
	}
	return errors.Join(saveErr, unbindErr)
}
