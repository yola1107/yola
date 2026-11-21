package player

import "sync"

type Monitor struct {
	Num     int64
	Offline int64
}

type Manager struct {
	players sync.Map // key: playerID, value: *Player
}

func (m *Manager) AddIfAbsent(p *Player) (*Player, bool) {
	if p == nil {
		return nil, false
	}
	current, loaded := m.players.LoadOrStore(p.GetPlayerID(), p)
	actual, _ := current.(*Player)
	return actual, !loaded
}

func (m *Manager) GetByID(id int64) *Player {
	value, _ := m.players.Load(id)
	p, _ := value.(*Player)
	return p
}

func (m *Manager) RemoveIfSame(p *Player) bool {
	return p != nil && m.players.CompareAndDelete(p.GetPlayerID(), p)
}

func (m *Manager) All() []*Player {
	var result []*Player
	m.players.Range(func(_, value any) bool {
		p, _ := value.(*Player)
		result = append(result, p)
		return true
	})
	return result
}

func (m *Manager) Monitor() Monitor {
	var all, offline int64
	m.players.Range(func(_, value any) bool {
		all++
		p, _ := value.(*Player)
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
