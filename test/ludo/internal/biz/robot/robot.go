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
	"yola/test/ludo/pkg/codes"
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

func (m *Manager) Counts() (all, gaming, free int32) {
	all = countPlayers(&m.all)
	free = countPlayers(&m.free)
	return all, all - free, free
}

func (m *Manager) load() {
	cfg := m.rc.Robot
	if !cfg.Open || cfg.Num <= 0 {
		return
	}
	current := countPlayers(&m.all)
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
	excess := countPlayers(&m.all) - maxNum
	toRelease := min(excess, defaultBatchReleaseCount)
	if toRelease <= 0 {
		return
	}

	m.free.Range(func(k, v any) bool {
		if v.(*player.Player).GetTableID() > 0 {
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

	seated := countPlayers(&m.all) - countPlayers(&m.free)
	if seated >= m.rc.Robot.MinPlayCount {
		return
	}

	players := make([]*player.Player, 0, countPlayers(&m.free))
	m.free.Range(func(_, value any) bool {
		p, ok := value.(*player.Player)
		if !ok || p.GetTableID() > 0 {
			return true
		}
		if code, _ := table.CheckRoomLimit(p, m.rc.Game); code != codes.Success {
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

func countPlayers(players *sync.Map) int32 {
	var count int32
	players.Range(func(_, _ any) bool {
		count++
		return true
	})
	return count
}
