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

func (p *Player) UpdateSession(sess Session) {
	p.sessionMu.Lock()
	defer p.sessionMu.Unlock()
	p.session = sess
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
