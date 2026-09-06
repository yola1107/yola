package robot

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"yola/test/internal/xgo"
	"yola/test/ludo/internal/biz/player"
	"yola/test/ludo/internal/biz/table"
	"yola/test/ludo/internal/conf"
)

const (
	defaultBatchLoadCount    = 100
	defaultBatchReleaseCount = 100
)

type Manager struct {
	rc   *conf.Room
	repo Repo

	all  sync.Map // map[playerID]*player.Player
	free sync.Map // map[playerID]*player.Player
}

func NewManager(room *conf.Room, repo Repo) *Manager {
	return &Manager{
		rc:   room,
		repo: repo,
	}
}

// Run 定期维护机器人，直到 ctx 取消。
func (m *Manager) Run(ctx context.Context) {
	if ctx == nil {
		return
	}
	maintenanceTicker := time.NewTicker(5 * time.Second)
	loginTicker := time.NewTicker(3 * time.Second)
	defer maintenanceTicker.Stop()
	defer loginTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-maintenanceTicker.C:
			m.load()
			m.release()
		case <-loginTicker.C:
			m.login(ctx)
		}
	}
}

func (m *Manager) load() {
	cfg := m.rc.Robot
	if !cfg.Open || cfg.Num <= 0 {
		return
	}
	current := m.countAll()
	toLoad := min(cfg.Num-current, defaultBatchLoadCount)
	if toLoad <= 0 {
		return
	}

	idStart, idEnd := cfg.IdBegin, cfg.IdBegin+int64(cfg.Num*2)
	for id := idStart; id <= idEnd && toLoad > 0; id++ {
		if _, exists := m.all.Load(id); exists {
			continue
		}
		p, err := m.repo.CreateRobot(&player.Raw{ID: id, IsRobot: true})
		if err != nil || p == nil {
			slog.Error("initialize robot", "uid", id, "error", err)
			continue
		}
		m.reset(p)
		m.all.Store(id, p)
		m.free.Store(id, p)
		toLoad--
	}
}

func (m *Manager) release() {
	maxNum := int32(0)
	if cfg := m.rc.Robot; cfg.Open {
		maxNum = cfg.Num
	}
	excess := m.countAll() - maxNum
	toRelease := min(excess, defaultBatchReleaseCount)
	if toRelease <= 0 {
		return
	}

	m.free.Range(func(k, v any) bool {
		p := v.(*player.Player)
		if p.GetTableID() > 0 {
			return true
		}
		m.all.Delete(k)
		m.free.Delete(k)
		toRelease--
		return toRelease > 0
	})
}

func (m *Manager) login(ctx context.Context) {
	if !m.rc.Robot.Open {
		return
	}

	seated := m.countAll() - m.countFree()
	if seated >= m.rc.Robot.MinPlayCount {
		return
	}

	players := make([]*player.Player, 0, m.countFree())
	m.free.Range(func(_, value any) bool {
		p, ok := value.(*player.Player)
		if !ok || p.GetTableID() > 0 {
			return true
		}
		if code, _ := table.CheckRoomLimit(p, m.rc.Game); code != 0 {
			return true
		}
		players = append(players, p)
		return true
	})
	if len(players) == 0 {
		return
	}
	entered, err := m.repo.EnterRobots(ctx, players)
	for _, uid := range entered {
		m.free.Delete(uid)
	}
	if err != nil {
		slog.Error("enter robots", "error", err)
	}
}

// Leave 机器人离开桌子，放回空闲池
func (m *Manager) Leave(uid int64) bool {
	value, ok := m.all.Load(uid)
	if !ok {
		return false
	}
	p, ok := value.(*player.Player)
	if !ok {
		m.all.Delete(uid)
		m.free.Delete(uid)
		return false
	}
	if _, alreadyFree := m.free.Load(uid); alreadyFree {
		return true
	}
	m.reset(p)
	m.free.Store(uid, p)
	return true
}

func (m *Manager) reset(p *player.Player) {
	minMoney := max(m.rc.Robot.MinMoney, m.rc.Game.MinMoney)
	maxMoney := m.rc.Robot.MaxMoney
	if m.rc.Game.MaxMoney != -1 {
		maxMoney = min(maxMoney, m.rc.Game.MaxMoney)
	}
	money := p.GetBaseData().Money
	if money < minMoney || money > maxMoney {
		p.GetBaseData().Money = float64(int64(xgo.RandFloat(minMoney, maxMoney)))
	}
}

func (m *Manager) Counts() (all, gaming, free int32) {
	all = m.countAll()
	free = m.countFree()
	return all, all - free, free
}

func (m *Manager) countAll() int32 {
	var count int32
	m.all.Range(func(_, _ any) bool {
		count++
		return true
	})
	return count
}

func (m *Manager) countFree() int32 {
	var count int32
	m.free.Range(func(_, _ any) bool {
		count++
		return true
	})
	return count
}
