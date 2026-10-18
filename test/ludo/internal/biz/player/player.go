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
	RenewNode(context.Context) error
	UnbindNode(context.Context) error
	Push(context.Context, int32, proto.Message) error
}

type Player struct {
	isRobot         bool
	sessionMu       sync.RWMutex
	session         Session
	sessionVersion  uint64
	nodeBindingLost bool
	gameData        *GameData
	baseData        *BaseData
	exiting         atomic.Bool
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
	p.sessionVersion++
}

// SessionForRenewal 返回本次续租的会话及代次；失权玩家不再续租。
func (p *Player) SessionForRenewal() (Session, uint64) {
	p.sessionMu.RLock()
	defer p.sessionMu.RUnlock()
	if p.nodeBindingLost {
		return nil, p.sessionVersion
	}
	return p.session, p.sessionVersion
}

// LoseNodeBinding 由桌内 owner 处理当前代的失权，迟到结果不能覆盖重连。
func (p *Player) LoseNodeBinding(version uint64) bool {
	p.sessionMu.Lock()
	defer p.sessionMu.Unlock()
	if p.sessionVersion != version || p.nodeBindingLost {
		return false
	}
	p.SetOffline(true)
	p.session = nil
	p.sessionVersion++
	p.nodeBindingLost = true
	return true
}

// NodeBindingLost 表示旧驻留只能本地回收，不能再保存或解绑。
func (p *Player) NodeBindingLost() bool {
	p.sessionMu.RLock()
	defer p.sessionMu.RUnlock()
	return p.nodeBindingLost
}

// ConfirmNodeBinding 仅在桌内重新绑定和重入成功后恢复驻留。
func (p *Player) ConfirmNodeBinding() {
	p.sessionMu.Lock()
	defer p.sessionMu.Unlock()
	p.nodeBindingLost = false
}

func (p *Player) TryBeginExit() bool {
	return p != nil && p.exiting.CompareAndSwap(false, true)
}

func (p *Player) CancelExit() {
	if p != nil {
		p.exiting.Store(false)
	}
}

func (p *Player) GetIP() string {
	// Yola Session deliberately exposes authenticated identity, not transport peer details.
	return ""
}
