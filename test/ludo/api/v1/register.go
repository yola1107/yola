package v1

import (
	"context"

	"yola/node"
)

// GameServer 定义通过 Node 暴露的 Ludo 命令处理器。
type GameServer interface {
	OnLoginReq(context.Context, *LoginReq) (*LoginRsp, error)
	OnLogoutReq(context.Context, *LogoutReq) (*LogoutRsp, error)
	OnReadyReq(context.Context, *ReadyReq) (*ReadyRsp, error)
	OnSwitchTableReq(context.Context, *SwitchTableReq) (*SwitchTableRsp, error)
	OnSceneReq(context.Context, *SceneReq) (*SceneRsp, error)
	OnChatReq(context.Context, *ChatReq) (*ChatRsp, error)
	OnHostingReq(context.Context, *HostingReq) (*HostingRsp, error)
	OnForwardReq(context.Context, *ForwardReq) (*ForwardRsp, error)
	OnDiceReq(context.Context, *DiceReq) (*DiceRsp, error)
	OnMoveReq(context.Context, *MoveReq) (*MoveRsp, error)
}

// RegisterGameServer 在 Node 注册 Ludo 命令。
func RegisterGameServer(server *node.Server, game GameServer) {
	node.Register(server, GameCommand_CmdLogin, game.OnLoginReq)
	node.Register(server, GameCommand_CmdLogout, game.OnLogoutReq)
	node.Register(server, GameCommand_CmdReady, game.OnReadyReq)
	node.Register(server, GameCommand_CmdSwitchTable, game.OnSwitchTableReq)
	node.Register(server, GameCommand_CmdScene, game.OnSceneReq)
	node.Register(server, GameCommand_CmdChat, game.OnChatReq)
	node.Register(server, GameCommand_CmdHosting, game.OnHostingReq)
	node.Register(server, GameCommand_CmdForward, game.OnForwardReq)
	node.Register(server, GameCommand_CmdDice, game.OnDiceReq)
	node.Register(server, GameCommand_CmdMove, game.OnMoveReq)
}
