package player

import (
	"sync"
)

type Monitor struct {
	Num     int64
	Offline int64
}

type Manager struct {
	players sync.Map // key: playerID, value: *Player
}

func NewManager() *Manager {
	return &Manager{}
}

func (m *Manager) AddIfAbsent(p *Player) (*Player, bool) {
	if p == nil {
		return nil, false
	}
	current, loaded := m.players.LoadOrStore(p.GetPlayerID(), p)
	return current.(*Player), !loaded
}

func (m *Manager) GetByID(id int64) *Player {
	if p, ok := m.players.Load(id); ok {
		return p.(*Player)
	}
	return nil
}

func (m *Manager) RemoveIfSame(p *Player) bool {
	return p != nil && m.players.CompareAndDelete(p.GetPlayerID(), p)
}

func (m *Manager) All() []*Player {
	var result []*Player
	m.players.Range(func(_, value interface{}) bool {
		result = append(result, value.(*Player))
		return true
	})
	return result
}

func (m *Manager) Monitor() Monitor {
	var all, offline int64
	m.players.Range(func(_, value interface{}) bool {
		all++
		p := value.(*Player)
		if p != nil && p.IsOffline() {
			offline++
		}
		return true
	})
	return Monitor{
		Num:     all,
		Offline: offline,
	}
}
