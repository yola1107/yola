package biz

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"yola/test/internal/xgo"
	"yola/test/whot/api/v1"
	"yola/test/whot/internal/biz/player"
	"yola/test/whot/internal/biz/table"
	"yola/test/whot/internal/conf"
	"yola/test/whot/pkg/codes"
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
	uid, err := SessionUID(sess)
	if err != nil {
		return nil
	}
	p := uc.pm.GetByID(uid)
	if p == nil {
		return nil
	}
	current := p.GetSession()
	if current == nil || current.BindingToken() != sess.BindingToken() {
		return nil
	}
	p.SetOffline(true)
	for range 2 {
		if p.GetTableID() <= 0 {
			return nil
		}
		err = uc.tm.CallPlayer(ctx, p, func(gameTable *table.Table) error {
			current := p.GetSession()
			if current == nil || current.BindingToken() != sess.BindingToken() {
				p.SetOffline(false)
				return nil
			}
			gameTable.OnOffline(p)
			return nil
		})
		if !errors.Is(err, table.ErrPlayerRouteChanged) {
			return err
		}
	}
	return err
}

func (uc *Usecase) Login(ctx context.Context, sess player.Session, uid int64, token string) (*v1.LoginRsp, error) {
	current := uc.pm.GetByID(uid)
	if current == nil {
		return uc.enterRoom(ctx, sess, uid, token)
	}
	if token == "" {
		return loginResponse(current, uid, codes.TokenFail, "TOKEN_FAIL"), nil
	}
	if current.GetTableID() > 0 {
		reply, err := uc.reconnect(ctx, sess, current)
		if err == nil || (!errors.Is(err, table.ErrTableNotFound) && !errors.Is(err, table.ErrPlayerRouteChanged)) {
			return reply, err
		}
	}
	if current.GetTableID() >= player.TableIDPending || !current.TryBeginExit() {
		return loginResponse(current, uid, codes.PlayerAlreadyInTable, "PLAYER_ALREADY_IN_TABLE"), nil
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
		exitErr := gameTable.OnExitGame(p, codes.Success, "logout")
		if errors.Is(exitErr, table.ErrExitGameRejected) {
			return nil
		}
		exited = exitErr == nil
		return exitErr
	})
	if err != nil {
		return nil, fmt.Errorf("logout player %d: %w", uid, err)
	}
	if !exited {
		return &v1.LogoutRsp{Code: codes.ExitTableFail, Msg: "EXIT_TABLE_FAIL", UserID: uid}, nil
	}
	return &v1.LogoutRsp{Code: codes.Success, UserID: uid}, nil
}

func (uc *Usecase) Ready(ctx context.Context, uid int64, ready bool) (*v1.ReadyRsp, error) {
	p, err := uc.managedPlayer(uid)
	if err != nil {
		return nil, err
	}
	var reply *v1.ReadyRsp
	err = uc.callPlayer(ctx, p, func(gameTable *table.Table) error {
		if !gameTable.OnReadyReq(p, ready) {
			return ErrReadyRejected
		}
		reply = &v1.ReadyRsp{UserID: uid, IsReady: ready}
		return nil
	})
	return reply, err
}

func (uc *Usecase) SwitchTable(ctx context.Context, uid int64) (*v1.SwitchTableRsp, error) {
	p, err := uc.managedPlayer(uid)
	if err != nil {
		return nil, err
	}
	code, message, err := uc.tm.Switch(ctx, p, uc.rc.Game)
	if err != nil {
		if p.GetTableID() == player.TableIDDetached && uc.pm.GetByID(uid) == p && p.TryBeginExit() {
			err = errors.Join(err, uc.LogoutGame(p, codes.ExitTableFail, "SWITCH_TABLE_RECOVERY_FAILED"))
		}
		return nil, fmt.Errorf("switch player %d table: %w", uid, mapTableCallError(err))
	}
	return &v1.SwitchTableRsp{Code: code, Msg: message, UserID: uid}, nil
}

func (uc *Usecase) Scene(ctx context.Context, uid int64) (*v1.SceneRsp, error) {
	p, err := uc.managedPlayer(uid)
	if err != nil {
		return nil, err
	}
	var reply *v1.SceneRsp
	err = uc.callPlayer(ctx, p, func(gameTable *table.Table) error {
		reply = gameTable.OnSceneReq(p)
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
		if !gameTable.OnChatReq(p, req) {
			return ErrChatRejected
		}
		reply = &v1.ChatRsp{UserID: int32(uid), OpType: req.OpType, FaceID: req.FaceID, Msg: req.Msg}
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
		if !gameTable.OnHosting(p, hosting) {
			return ErrHostingRejected
		}
		status := int32(1)
		if hosting {
			status = 2
		}
		reply = &v1.HostingRsp{ChairID: p.GetChairID(), Status: status, AiNum: p.GetTimeoutCnt()}
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
		gameTable.BroadcastForwardRsp(p, req.Type, req.Msg)
		reply = &v1.ForwardRsp{Type: req.Type, Msg: req.Msg}
		return nil
	})
	return reply, err
}

func (uc *Usecase) PlayerAction(ctx context.Context, uid int64, req *v1.PlayerActionReq) (*v1.PlayerActionRsp, error) {
	p, err := uc.managedPlayer(uid)
	if err != nil {
		return nil, err
	}
	var reply *v1.PlayerActionRsp
	err = uc.callPlayer(ctx, p, func(gameTable *table.Table) error {
		code, message := codes.Success, ""
		if !gameTable.OnPlayerActionReq(p, req, false) {
			code, message = codes.Fail, "PLAYER_ACTION_REJECTED"
		}
		reply = &v1.PlayerActionRsp{
			Code:    code,
			Message: message,
			UserId:  uid,
			ChairId: p.GetChairID(),
			Action:  req.Action,
		}
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
		return nil, fmt.Errorf("%w: player %d", ErrPlayerNotFound, uid)
	}
	return p, nil
}

func mapTableCallError(err error) error {
	if errors.Is(err, table.ErrTableNotFound) || errors.Is(err, table.ErrPlayerRouteChanged) {
		return fmt.Errorf("%w: %v", ErrInvalidPlayerState, err)
	}
	return err
}

func (uc *Usecase) reconnect(ctx context.Context, sess player.Session, p *player.Player) (*v1.LoginRsp, error) {
	if err := sess.BindNode(ctx); err != nil {
		return nil, fmt.Errorf("bind reconnecting player %d: %w", p.GetPlayerID(), err)
	}
	lifecycleCtx, cancelLifecycle := context.WithTimeout(context.Background(), playerCleanupTimeout)
	defer cancelLifecycle()
	err := uc.tm.CallPlayer(lifecycleCtx, p, func(gameTable *table.Table) error {
		p.UpdateSession(sess)
		gameTable.ReEnter(p)
		return nil
	})
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("re-enter player %d: %w", p.GetPlayerID(), err),
			sess.UnbindNode(lifecycleCtx),
		)
	}
	return loginResponse(p, p.GetPlayerID(), codes.Success, "ReEnter"), nil
}

func (uc *Usecase) enterRoom(ctx context.Context, sess player.Session, uid int64, token string) (*v1.LoginRsp, error) {
	raw := &player.Raw{ID: uid, Session: sess, BaseData: &player.BaseData{UID: uid}}
	p, err := uc.createPlayer(ctx, raw)
	if err != nil {
		slog.Error("create player", "uid", uid, "error", err)
		return loginResponse(nil, uid, codes.CreatePlayerFail, "CREATE_PLAYER_FAIL"), nil
	}
	if token == "" {
		p.LogoutGame()
		return loginResponse(nil, uid, codes.TokenFail, "TOKEN_FAIL"), nil
	}
	if code, message := table.CheckRoomLimit(p, uc.rc.Game); code != codes.Success {
		p.LogoutGame()
		return loginResponse(nil, uid, code, message), nil
	}
	current, added := uc.pm.AddIfAbsent(p)
	if !added {
		p.LogoutGame()
		return loginResponse(current, uid, codes.PlayerAlreadyInTable, "PLAYER_ALREADY_IN_TABLE"), nil
	}
	if err = sess.BindNode(ctx); err != nil {
		uc.pm.RemoveIfSame(p)
		p.LogoutGame()
		return nil, fmt.Errorf("bind player %d: %w", uid, err)
	}

	lifecycleCtx, cancelLifecycle := context.WithTimeout(context.Background(), playerCleanupTimeout)
	defer cancelLifecycle()
	code, message, enterErr := uc.tm.Enter(lifecycleCtx, p)
	if enterErr == nil && code == codes.Success {
		return loginResponse(p, uid, code, message), nil
	}
	unbindErr := sess.UnbindNode(lifecycleCtx)
	uc.pm.RemoveIfSame(p)
	p.LogoutGame()
	if enterErr != nil {
		return nil, errors.Join(fmt.Errorf("enter player %d: %w", uid, enterErr), unbindErr)
	}
	if unbindErr != nil {
		return nil, errors.Join(
			fmt.Errorf("enter room failed: code=%d message=%s", code, message),
			fmt.Errorf("rollback node binding for player %d: %w", uid, unbindErr),
		)
	}
	return loginResponse(p, uid, code, message), nil
}

func loginResponse(p *player.Player, uid int64, code int32, msg string) *v1.LoginRsp {
	reply := &v1.LoginRsp{Code: code, Msg: msg, UserID: uid, ArenaID: int32(conf.ArenaID)}
	if p != nil {
		reply.TableID = p.GetTableID()
		reply.ChairID = p.GetChairID()
	}
	return reply
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
	p := player.New(raw)
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

	p.SetBaseData(base)
	if slog.Default().Enabled(context.Background(), slog.LevelDebug) {
		slog.Debug("create player", "player", p.Desc())
	}
	return p, nil
}

func (uc *Usecase) LogoutGame(p *player.Player, code int32, msg string) error {
	if p == nil {
		return nil
	}
	uid := p.GetPlayerID()
	if p.IsRobot() {
		uc.rm.Leave(uid)
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
	uc.finalizePlayer(p)
	slog.Info("logout game", "uid", uid, "code", code, "message", msg)
	return nil
}

func (uc *Usecase) cleanupPlayers(ctx context.Context) error {
	if ctx == nil {
		return errors.New("cleanup Whot players: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("cleanup Whot players: %w", err)
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
				uc.finalizePlayer(result.player)
			}
		case <-ctx.Done():
			cleanupErrors = append(cleanupErrors, fmt.Errorf("wait for Whot player cleanup: %w", ctx.Err()))
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
	var cleanupErr error
	if base := p.GetBaseData(); base == nil {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("save player %d: base data is missing", uid))
	} else {
		baseData := *base
		saveCtx, cancelSave := context.WithTimeout(ctx, playerCleanupOperationTimeout)
		err := uc.repo.SavePlayer(saveCtx, &baseData)
		cancelSave()
		if err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("save player %d: %w", uid, err))
		}
	}
	if sess := p.GetSession(); sess != nil {
		if err := ctx.Err(); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("unbind player %d: %w", uid, err))
		} else {
			unbindCtx, cancelUnbind := context.WithTimeout(ctx, playerCleanupOperationTimeout)
			err := sess.UnbindNode(unbindCtx)
			cancelUnbind()
			if err != nil {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("unbind player %d: %w", uid, err))
			}
		}
	}
	return cleanupErr
}

func (uc *Usecase) finalizePlayer(p *player.Player) {
	uc.pm.RemoveIfSame(p)
	p.LogoutGame()
}
