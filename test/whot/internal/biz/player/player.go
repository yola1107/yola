package player

import (
	"context"
	"sync"
	"sync/atomic"

	"google.golang.org/protobuf/proto"
)

// Session is the authenticated client capability retained by player state.
type Session interface {
	UID() string
	BindingToken() string
	BindNode(context.Context) error
	UnbindNode(context.Context) error
	Push(context.Context, int32, proto.Message) error
}

type Player struct {
	isRobot   bool
	sessionMu sync.RWMutex
	session   Session
	isOffline bool
	gameData  *GameData
	baseData  *BaseData
	exiting   atomic.Bool
}

type Raw struct {
	ID       int64
	IsRobot  bool
	Session  Session
	BaseData *BaseData
}

func New(raw *Raw) *Player {
	return &Player{
		isRobot:  raw.IsRobot,
		session:  raw.Session,
		gameData: &GameData{},
		baseData: raw.BaseData,
	}
}

func (p *Player) SetBaseData(baseData *BaseData) {
	p.baseData = baseData
}

func (p *Player) GetBaseData() *BaseData {
	return p.baseData
}

func (p *Player) IsRobot() bool {
	return p.isRobot
}

func (p *Player) GetSession() Session {
	p.sessionMu.RLock()
	defer p.sessionMu.RUnlock()
	return p.session
}

// UpdateSession 原子替换连接并恢复在线；登出时保留离线状态。
func (p *Player) UpdateSession(sess Session) {
	p.sessionMu.Lock()
	defer p.sessionMu.Unlock()
	p.session = sess
	if sess != nil {
		p.isOffline = false
	}
}

// MarkOffline 仅标记当前连接，避免旧连接通知覆盖重连状态。
func (p *Player) MarkOffline(bindingToken string) bool {
	p.sessionMu.Lock()
	defer p.sessionMu.Unlock()
	if p.session == nil || p.session.BindingToken() != bindingToken {
		return false
	}
	p.isOffline = true
	return true
}

func (p *Player) SetOffline(offline bool) {
	p.sessionMu.Lock()
	defer p.sessionMu.Unlock()
	p.isOffline = offline
}

func (p *Player) IsOffline() bool {
	p.sessionMu.RLock()
	defer p.sessionMu.RUnlock()
	return p.isOffline
}

func (p *Player) LogoutGame() {
	p.UpdateSession(nil)
}

func (p *Player) TryBeginExit() bool {
	return p != nil && p.exiting.CompareAndSwap(false, true)
}

func (p *Player) CancelExit() {
	if p != nil {
		p.exiting.Store(false)
	}
}
