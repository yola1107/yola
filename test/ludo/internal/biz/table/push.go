package table

import (
	"context"
	"errors"
	"log/slog"
	"strconv"

	"yola/test/internal/xgo"
	"yola/test/ludo/api/v1"
	"yola/test/ludo/internal/biz/player"
	"yola/test/ludo/internal/model"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

var ErrPlayerDisconnected = errors.New("player session is disconnected")

func (t *Table) sendPacket(p *player.Player, cmd v1.GameCommand, msg proto.Message) error {
	if p == nil || p.IsOffline() || t.playerAt(p.GetChairID()) != p {
		return nil
	}
	if p.IsRobot() {
		t.robotLogic.onMessage(p, cmd, msg)
		return nil
	}
	var err error
	if t.manager != nil && t.manager.pusher != nil {
		err = t.manager.pusher.PushToUID(
			context.Background(),
			strconv.FormatInt(p.GetPlayerID(), 10),
			int32(cmd),
			msg,
		)
	} else {
		sess := p.GetSession()
		if sess == nil {
			return nil
		}
		err = sess.Push(context.Background(), int32(cmd), msg)
	}
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return errors.Join(ErrPlayerDisconnected, err)
		}
		slog.Warn("push packet", "command", int32(cmd), "uid", p.GetPlayerID(), "error", err)
	}
	return nil
}

func (t *Table) sendToAll(cmd v1.GameCommand, msg proto.Message) {
	var disconnectedPlayers []*player.Player
	for _, recipient := range t.seats {
		if recipient == nil {
			continue
		}
		if err := t.sendPacket(recipient, cmd, msg); errors.Is(err, ErrPlayerDisconnected) {
			disconnectedPlayers = append(disconnectedPlayers, recipient)
		}
	}
	t.offlinePlayers(disconnectedPlayers)
}

func (t *Table) sendToAllExcept(cmd v1.GameCommand, msg proto.Message, excludedUID int64) {
	var disconnectedPlayers []*player.Player
	for _, recipient := range t.seats {
		if recipient == nil || recipient.GetPlayerID() == excludedUID {
			continue
		}
		if err := t.sendPacket(recipient, cmd, msg); errors.Is(err, ErrPlayerDisconnected) {
			disconnectedPlayers = append(disconnectedPlayers, recipient)
		}
	}
	t.offlinePlayers(disconnectedPlayers)
}

func (t *Table) offlinePlayers(players []*player.Player) {
	for _, p := range players {
		if p == nil || p.IsOffline() || t.playerAt(p.GetChairID()) != p {
			continue
		}
		if err := t.Offline(p); err != nil {
			slog.Error("handle disconnected player", "uid", p.GetPlayerID(), "error", err)
		}
	}
}

func (t *Table) broadcastUserInfo(p *player.Player) error {
	if err := t.sendUserInfo(p, p); err != nil {
		return errors.Join(err, t.Offline(p))
	}
	var disconnectedPlayers []*player.Player
	var targetErr error
	for chair, seatedPlayer := range t.seats {
		if seatedPlayer != nil && chair != int(p.GetChairID()) {
			if err := t.sendUserInfo(p, seatedPlayer); errors.Is(err, ErrPlayerDisconnected) {
				disconnectedPlayers = append(disconnectedPlayers, seatedPlayer)
			}
			if err := t.sendUserInfo(seatedPlayer, p); err != nil {
				targetErr = err
				break
			}
		}
	}
	if targetErr != nil {
		targetErr = errors.Join(targetErr, t.Offline(p))
	}
	t.offlinePlayers(disconnectedPlayers)
	return targetErr
}

func (t *Table) sendUserInfo(src *player.Player, dst *player.Player) error {
	return t.sendPacket(dst, v1.GameCommand_CmdUserInfoPush, &v1.UserInfoPush{
		UserID:    src.GetPlayerID(),
		ChairID:   src.GetChairID(),
		UserName:  src.GetNickName(),
		Money:     src.GetAllMoney(),
		Avatar:    src.GetAvatar(),
		AvatarUrl: src.GetAvatarURL(),
		Vip:       src.GetVipGrade(),
		Status:    int32(src.GetStatus()),
		Ip:        src.GetIP(),
	})
}

func (t *Table) BroadcastReadyPush(p *player.Player, rsp *v1.ReadyRsp) {
	t.sendToAllExcept(v1.GameCommand_CmdReadyPush, rsp, p.GetPlayerID())
}

func (t *Table) BroadcastChatPush(p *player.Player, rsp *v1.ChatRsp) {
	t.sendToAllExcept(v1.GameCommand_CmdChatPush, rsp, p.GetPlayerID())
}

func (t *Table) BroadcastHostingPush(p *player.Player, rsp *v1.HostingRsp) {
	t.sendToAllExcept(v1.GameCommand_CmdHostingPush, rsp, p.GetPlayerID())
}

func (t *Table) BroadcastForwardPush(p *player.Player, rsp *v1.ForwardRsp) {
	t.sendToAllExcept(v1.GameCommand_CmdForwardPush, rsp, p.GetPlayerID())
}

// 广播玩家断线信息
func (t *Table) broadcastUserOffline(p *player.Player) {
	t.sendToAll(v1.GameCommand_CmdUserOfflinePush, &v1.UserOfflinePush{
		UserID:    p.GetPlayerID(),
		IsOffline: p.IsOffline(),
	})
}

// 玩家离桌推送
func (t *Table) broadcastUserQuitPush(p *player.Player) {
	t.sendToAllExcept(v1.GameCommand_CmdPlayerQuitPush, &v1.PlayerQuitPush{
		UserID:  p.GetPlayerID(),
		ChairID: p.GetChairID(),
	}, p.GetPlayerID())
}

// 发牌推送
func (t *Table) dispatchCardPush(players []*player.Player) {
	pieces := t.boardPieces()
	var disconnected []*player.Player
	for _, p := range players {
		if p == nil || !p.IsGaming() {
			continue
		}
		err := t.sendPacket(p, v1.GameCommand_CmdSendCardPush, &v1.SendCardPush{
			UserID:     p.GetPlayerID(),
			FirstChair: t.firstChair,
			Color:      p.GetColor(),
			Pieces:     pieces,
		})
		if errors.Is(err, ErrPlayerDisconnected) {
			disconnected = append(disconnected, p)
		}
	}
	t.offlinePlayers(disconnected)
}

func (t *Table) boardPieces() []*v1.Piece {
	if t.board == nil {
		return nil
	}
	pieces := []*v1.Piece(nil)
	for _, piece := range t.board.Pieces() {
		pieces = append(pieces, &v1.Piece{
			Id:     piece.ID(),
			Pos:    piece.Pos(),
			Color:  piece.Color(),
			Status: piece.Status(),
		})
	}
	return pieces
}

func (t *Table) Scene() *v1.SceneRsp {
	return &v1.SceneRsp{
		BaseScore:   t.rc.Game.BaseMoney,
		Stage:       int32(t.stage.State()),
		Timeout:     int64(t.stage.Remaining().Seconds()),
		Active:      t.activeChair,
		FirstChair:  t.firstChair,
		BoardConfig: t.getBoardConfig(),
		Players:     t.getPlayersScene(),
		Pieces:      t.boardPieces(),
	}
}

func (t *Table) sendScene(p *player.Player) error {
	err := t.sendPacket(p, v1.GameCommand_CmdScenePush, t.Scene())
	if errors.Is(err, ErrPlayerDisconnected) {
		return errors.Join(err, t.Offline(p))
	}
	return err
}

func (t *Table) getBoardConfig() *v1.BoardConfig {
	common := []int32(nil)
	for i := 0; i < model.TotalPositions; i++ {
		common = append(common, int32(i))
	}
	var safe []int32
	if len(model.SafePositions) > 0 {
		safe = make([]int32, 0, len(model.SafePositions))
	}
	for k := range model.SafePositions {
		safe = append(safe, k)
	}
	return &v1.BoardConfig{
		Common: common,
		Home:   []int32{-1, -1, -1, -1},
		Entry:  model.EntryPoints,
		Safe:   safe,
		End:    model.HomeStartIndices,
		Color:  []int32{0, 1, 2, 3},
	}
}

func (t *Table) getPlayersScene() []*v1.PlayerInfo {
	var players []*v1.PlayerInfo
	for _, p := range t.seats {
		if p == nil {
			continue
		}
		players = append(players, t.getScene(p))
	}
	return players
}

func (t *Table) getScene(p *player.Player) *v1.PlayerInfo {
	if p == nil {
		return nil
	}
	return &v1.PlayerInfo{
		UserId:    p.GetPlayerID(),
		ChairId:   p.GetChairID(),
		Status:    int32(p.GetStatus()),
		Hosting:   p.GetTimeoutCnt() > 0,
		Offline:   p.IsOffline(),
		Color:     p.GetColor(),
		DiceList:  t.getDiceList(p),
		CanAction: t.getCanAction(p),
	}
}

func (t *Table) getDiceList(p *player.Player) []*v1.Dice {
	list := p.GetDiceSlot()
	if p.GetChairID() != t.activeChair {
		list = p.GetLastDiceSlot()
	}

	var dices []*v1.Dice
	if len(list) > 0 {
		dices = make([]*v1.Dice, 0, len(list))
	}
	for _, v := range list {
		dices = append(dices, &v1.Dice{
			Value: v.Value,
			Used:  v.Used,
		})
	}
	return dices
}

// 当前活动玩家推送
func (t *Table) broadcastActivePlayerPush() {
	push := &v1.ActivePush{
		Stage:   int32(t.stage.State()),
		Timeout: int64(t.stage.Remaining().Seconds()),
		Active:  t.activeChair,
	}
	if active := t.activePlayer(); active != nil {
		push.UnusedDices = active.UnusedDice()
		push.CanAction = t.getCanAction(active)
		debugEnabled := debugLogEnabled()
		fileLogEnabled := t.mLog.enabled()
		movablePaths := ""
		if (debugEnabled || fileLogEnabled) && push.CanAction == v1.ACTION_TYPE_AcMove {
			movablePaths = xgo.ToJSON(t.board.CalcAllMovable(active.GetColor(), active.UnusedDice()))
		}
		if fileLogEnabled {
			t.mLog.activePush(active, push.CanAction, "", movablePaths)
		}
		if debugEnabled {
			slog.Debug("push active player",
				"player", active.Desc(),
				"action", push.CanAction.String(),
				"result", movablePaths,
			)
		}
	}
	t.sendToAll(v1.GameCommand_CmdActivePush, push)
}

func (t *Table) getCanAction(p *player.Player) v1.ACTION_TYPE {
	if p == nil || !p.IsGaming() || p.GetChairID() != t.activeChair || p.IsFinish() {
		return 0
	}
	switch t.stage.State() {
	case StDice:
		return v1.ACTION_TYPE_AcDice
	case StMove:
		return v1.ACTION_TYPE_AcMove
	default:
		return 0
	}
}

func (t *Table) diceResponse(p *player.Player, dice int32) *v1.DiceRsp {
	slots := p.GetDiceSlot()
	var diceList []*v1.Dice
	if len(slots) > 0 {
		diceList = make([]*v1.Dice, 0, len(slots))
	}
	for _, v := range slots {
		diceList = append(diceList, &v1.Dice{
			Value: v.Value,
			Used:  v.Used,
		})
	}
	return &v1.DiceRsp{
		Code:     0,
		Msg:      "",
		Uid:      p.GetPlayerID(),
		Dice:     dice,
		DiceList: diceList,
	}
}

func (t *Table) BroadcastDicePush(requester *player.Player, rsp *v1.DiceRsp) {
	if requester == nil {
		t.sendToAll(v1.GameCommand_CmdDicePush, rsp)
		return
	}
	t.sendToAllExcept(v1.GameCommand_CmdDicePush, rsp, requester.GetPlayerID())
}

func (t *Table) moveResponse(p *player.Player, dice int32, step *model.Step) *v1.MoveRsp {
	move := &v1.DiceMove{}
	killed := []*v1.DiceMove(nil)

	if step != nil {
		move = &v1.DiceMove{
			PlayerId: p.GetPlayerID(),
			PieceId:  step.ID,
			From:     step.From,
			To:       step.To,
		}
		for _, kill := range step.Killed {
			killed = append(killed, &v1.DiceMove{
				PieceId: kill.ID,
				From:    kill.From,
				To:      kill.To,
			})
		}
	}

	return &v1.MoveRsp{
		Code:      0,
		Msg:       "",
		DiceValue: dice,
		Move:      move,
		Killed:    killed,
		Pieces:    t.boardPieces(),
	}
}

func (t *Table) BroadcastMovePush(requester *player.Player, rsp *v1.MoveRsp) {
	if requester == nil {
		t.sendToAll(v1.GameCommand_CmdMovePush, rsp)
		return
	}
	t.sendToAllExcept(v1.GameCommand_CmdMovePush, rsp, requester.GetPlayerID())
}
