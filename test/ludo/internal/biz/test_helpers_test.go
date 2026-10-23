package biz

import (
	"context"
	"sync"
	"testing"

	v1 "yola/test/ludo/api/v1"
	"yola/test/ludo/internal/biz/player"
	"yola/test/ludo/internal/biz/table"
	"yola/test/ludo/internal/conf"

	"google.golang.org/protobuf/proto"
)

type memoryPlayerRepo struct {
	mu          sync.Mutex
	base        *player.BaseData
	loadErr     error
	saveErr     error
	saveCount   int
	loadBarrier *sync.WaitGroup
}

func (repo *memoryPlayerRepo) SavePlayer(_ context.Context, base *player.BaseData) error {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	repo.saveCount++
	if base != nil {
		baseCopy := *base
		repo.base = &baseCopy
	}
	return repo.saveErr
}

func (repo *memoryPlayerRepo) LoadPlayer(_ context.Context, uid int64) (*player.BaseData, error) {
	if repo.loadBarrier != nil {
		repo.loadBarrier.Done()
		repo.loadBarrier.Wait()
	}
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if repo.loadErr != nil {
		return nil, repo.loadErr
	}
	if repo.base == nil {
		return nil, ErrPlayerNotFound
	}
	baseCopy := *repo.base
	baseCopy.UID = uid
	return &baseCopy, nil
}

func (repo *memoryPlayerRepo) saved() int {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	return repo.saveCount
}

type recordingSession struct {
	uid          string
	bindingToken string
	bindErr      error
	unbindErr    error
	pushErr      error

	mu      sync.Mutex
	binds   int
	unbinds int
	pushes  []int32
}

var _ player.Session = (*recordingSession)(nil)

func (sess *recordingSession) UID() string { return sess.uid }

func (sess *recordingSession) BindingToken() string { return sess.bindingToken }

func (sess *recordingSession) BindNode(context.Context) error {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.binds++
	return sess.bindErr
}

func (*recordingSession) RenewNode(context.Context) error { return nil }

func (sess *recordingSession) UnbindNode(context.Context) error {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.unbinds++
	return sess.unbindErr
}

func (sess *recordingSession) Push(_ context.Context, command int32, _ proto.Message) error {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.pushes = append(sess.pushes, command)
	return sess.pushErr
}

func (sess *recordingSession) counts() (int, int) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.binds, sess.unbinds
}

func (sess *recordingSession) hasPush(command v1.GameCommand) bool {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	for _, pushed := range sess.pushes {
		if pushed == int32(command) {
			return true
		}
	}
	return false
}

func (sess *recordingSession) setPushError(err error) {
	sess.mu.Lock()
	sess.pushErr = err
	sess.mu.Unlock()
}

func newTestUsecase(t *testing.T, tableCount, chairCount int32) (*Usecase, *memoryPlayerRepo) {
	t.Helper()
	room := testRoom(tableCount, chairCount)
	repo := &memoryPlayerRepo{base: &player.BaseData{UID: 42, Money: 500}}
	uc := &Usecase{
		repo: repo,
		rc:   room,
		pm:   player.NewManager(),
	}
	uc.tm = table.NewManager("ludo-test", room, uc)
	if err := uc.tm.Start(context.Background()); err != nil {
		t.Fatalf("start table manager: %v", err)
	}
	t.Cleanup(func() { _ = uc.tm.Close(context.Background()) })
	return uc, repo
}

func testRoom(tableCount, chairCount int32) *conf.Room {
	return &conf.Room{
		Table:    &conf.Room_Table{TableNum: tableCount, ChairNum: chairCount},
		Game:     &conf.Room_Game{MinMoney: 100, MaxMoney: 1000, BaseMoney: 100},
		Robot:    &conf.Room_Robot{},
		LogCache: &conf.Room_LogCache{},
	}
}

func login(t *testing.T, uc *Usecase, raw *recordingSession, uid int64) *v1.LoginRsp {
	t.Helper()
	reply, err := uc.Login(context.Background(), raw, uid, "valid", 0)
	if err != nil {
		t.Fatalf("Login(%d): %v", uid, err)
	}
	return reply
}
