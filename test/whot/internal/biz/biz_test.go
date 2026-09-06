package biz

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"yola/test/whot/api/v1"
	"yola/test/whot/internal/biz/player"
	"yola/test/whot/internal/biz/table"
	"yola/test/whot/internal/conf"
	"yola/test/whot/pkg/codes"

	"google.golang.org/protobuf/proto"
)

func TestConcurrentLoginSeatsOnePlayer(t *testing.T) {
	room := &conf.Room{
		Table:    &conf.Room_Table{TableNum: 1, ChairNum: 2},
		Game:     &conf.Room_Game{MinMoney: 100, MaxMoney: 1000, BaseMoney: 100},
		Robot:    &conf.Room_Robot{},
		LogCache: &conf.Room_LogCache{},
	}
	uc, cleanup, err := NewUsecase(newConcurrentLoginRepo(), room)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)

	replies := make(chan *v1.LoginRsp, 2)
	errors := make(chan error, 2)
	var wait sync.WaitGroup
	for _, bindingToken := range []string{"binding-a", "binding-b"} {
		wait.Add(1)
		go func(bindingToken string) {
			defer wait.Done()
			reply, loginErr := uc.Login(context.Background(), &connectionSession{uid: "42", bindingToken: bindingToken}, 42, "valid")
			replies <- reply
			errors <- loginErr
		}(bindingToken)
	}
	wait.Wait()
	close(replies)
	close(errors)
	for loginErr := range errors {
		if loginErr != nil {
			t.Fatal(loginErr)
		}
	}
	var successes, rejected int
	for reply := range replies {
		switch reply.Code {
		case codes.Success:
			successes++
		case codes.PlayerAlreadyInTable:
			rejected++
		default:
			t.Fatalf("Login() code = %d", reply.Code)
		}
	}
	if successes != 1 || rejected != 1 {
		t.Fatalf("concurrent login: success=%d rejected=%d", successes, rejected)
	}
	if p := uc.pm.GetByID(42); p == nil || p.GetTableID() != 1 {
		t.Fatalf("managed player = %v", p)
	}
}

func TestStaleDisconnectDoesNotOfflineReconnectedPlayer(t *testing.T) {
	room := &conf.Room{
		Table:    &conf.Room_Table{TableNum: 1, ChairNum: 2},
		Game:     &conf.Room_Game{MinMoney: 100, MaxMoney: 1000, BaseMoney: 100},
		Robot:    &conf.Room_Robot{},
		LogCache: &conf.Room_LogCache{},
	}
	uc, cleanup, err := NewUsecase(new(usecaseRepoStub), room)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)

	first := &connectionSession{uid: "42", bindingToken: "binding-a"}
	if reply, err := uc.Login(context.Background(), first, 42, "valid"); err != nil || reply.Code != codes.Success {
		t.Fatalf("first Login() = (%+v, %v)", reply, err)
	}
	second := &connectionSession{uid: "42", bindingToken: "binding-b"}
	if reply, err := uc.Login(context.Background(), second, 42, "valid"); err != nil || reply.Code != codes.Success {
		t.Fatalf("reconnect Login() = (%+v, %v)", reply, err)
	}
	if err := uc.Disconnect(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	p := uc.pm.GetByID(42)
	if p == nil || p.GetSession() != second {
		t.Fatalf("current session = %v, want reconnect session", p)
	}
	if p.IsOffline() {
		t.Fatal("stale disconnect marked the reconnected player offline")
	}

	if err := uc.Disconnect(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if current := uc.pm.GetByID(42); current != nil && !current.IsOffline() {
		t.Fatal("current disconnect left the player online")
	}
}

func TestDisconnectDuringSwitchRemovesPlayerFromDestination(t *testing.T) {
	room := &conf.Room{
		Table:    &conf.Room_Table{TableNum: 2, ChairNum: 2},
		Game:     &conf.Room_Game{MinMoney: 100, MaxMoney: 1000, BaseMoney: 100},
		Robot:    &conf.Room_Robot{},
		LogCache: &conf.Room_LogCache{},
	}
	uc, cleanup, err := NewUsecase(new(usecaseRepoStub), room)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)

	sessions := make([]*connectionSession, 3)
	for index, uid := range []int64{41, 42, 43} {
		sessions[index] = &connectionSession{uid: fmt.Sprint(uid), bindingToken: fmt.Sprintf("binding-%d", uid)}
		if reply, loginErr := uc.Login(context.Background(), sessions[index], uid, "valid"); loginErr != nil || reply.Code != codes.Success {
			t.Fatalf("Login(%d) = (%+v, %v)", uid, reply, loginErr)
		}
	}
	p := uc.pm.GetByID(41)
	blocker := uc.pm.GetByID(43)
	if p.GetTableID() != 1 || blocker.GetTableID() != 2 {
		t.Fatalf("test routes = player:%d blocker:%d", p.GetTableID(), blocker.GetTableID())
	}

	targetBlocked := make(chan struct{})
	releaseTarget := make(chan struct{})
	blockDone := make(chan error, 1)
	go func() {
		blockDone <- uc.tm.CallPlayer(context.Background(), blocker, func(*table.Table) error {
			close(targetBlocked)
			<-releaseTarget
			return nil
		})
	}()
	<-targetBlocked

	switchDone := make(chan error, 1)
	go func() {
		_, switchErr := uc.SwitchTable(context.Background(), 41)
		switchDone <- switchErr
	}()
	waitForBizTest(t, func() bool { return p.GetTableID() == player.TableIDPending })
	if err := uc.Disconnect(context.Background(), sessions[0]); err != nil {
		t.Fatal(err)
	}
	close(releaseTarget)
	if err := <-blockDone; err != nil {
		t.Fatal(err)
	}
	if err := <-switchDone; !errors.Is(err, table.ErrPlayerDisconnected) {
		t.Fatalf("SwitchTable() error = %v, want %v", err, table.ErrPlayerDisconnected)
	}
	if current := uc.pm.GetByID(41); current != nil {
		t.Fatalf("disconnected player remained managed: %v", current)
	}
	if p.GetTableID() != player.TableIDDetached {
		t.Fatalf("disconnected player table = %d, want detached", p.GetTableID())
	}
}

func TestDrainPersistsUnbindsAndIsIdempotent(t *testing.T) {
	repo := new(usecaseRepoStub)
	uc, cleanup, err := NewUsecase(repo, whotDrainTestRoom())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	sess := &connectionSession{uid: "42", bindingToken: "binding"}
	reply, err := uc.Login(context.Background(), sess, 42, "valid")
	if err != nil || reply.Code != codes.Success {
		t.Fatalf("Login() = (%+v, %v)", reply, err)
	}
	p := uc.pm.GetByID(42)

	if err = uc.Drain(context.Background()); err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if err = uc.Drain(context.Background()); err != nil {
		t.Fatalf("repeated Drain() error = %v", err)
	}
	if repo.saved() != 1 || uc.pm.GetByID(42) != nil || p.GetSession() != nil {
		t.Fatalf("drain state: saves=%d player=%v session=%v", repo.saved(), uc.pm.GetByID(42), p.GetSession())
	}
	if sess.unbindCount() != 1 {
		t.Fatalf("UnbindNode() calls = %d, want 1", sess.unbindCount())
	}
	if err = uc.tm.CallPlayer(context.Background(), p, func(*table.Table) error { return nil }); err == nil {
		t.Fatal("Table Manager still accepted work after Drain")
	}
}

func TestDrainDoesNotCleanPlayersWhenTableStopTimesOut(t *testing.T) {
	repo := new(usecaseRepoStub)
	uc, cleanup, err := NewUsecase(repo, whotDrainTestRoom())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	sess := &connectionSession{uid: "42", bindingToken: "binding"}
	reply, err := uc.Login(context.Background(), sess, 42, "valid")
	if err != nil || reply.Code != codes.Success {
		t.Fatalf("Login() = (%+v, %v)", reply, err)
	}
	p := uc.pm.GetByID(42)
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseJob := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseJob)
	callDone := make(chan error, 1)
	go func() {
		callDone <- uc.tm.CallPlayer(context.Background(), p, func(*table.Table) error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err = uc.Drain(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Drain() error = %v, want %v", err, context.DeadlineExceeded)
	}
	if repo.saved() != 0 || uc.pm.GetByID(42) != p || p.GetSession() != sess {
		t.Fatalf("timed out drain cleaned player: saves=%d player=%v session=%v", repo.saved(), uc.pm.GetByID(42), p.GetSession())
	}
	if sess.unbindCount() != 0 {
		t.Fatalf("UnbindNode() calls = %d, want 0", sess.unbindCount())
	}

	releaseJob()
	if err = <-callDone; err != nil {
		t.Fatalf("blocked table call error = %v", err)
	}
	if err = uc.Drain(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("repeated Drain() error = %v, want recorded deadline", err)
	}
}

func TestDrainReturnsPlayerCleanupErrors(t *testing.T) {
	saveErr := errors.New("save failed")
	unbindErr := errors.New("unbind failed")
	repo := &usecaseRepoStub{saveErr: saveErr}
	uc, cleanup, err := NewUsecase(repo, whotDrainTestRoom())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	sess := &connectionSession{uid: "42", bindingToken: "binding", unbindErr: unbindErr}
	reply, err := uc.Login(context.Background(), sess, 42, "valid")
	if err != nil || reply.Code != codes.Success {
		t.Fatalf("Login() = (%+v, %v)", reply, err)
	}

	err = uc.Drain(context.Background())
	if !errors.Is(err, saveErr) || !errors.Is(err, unbindErr) {
		t.Fatalf("Drain() error = %v, want save and unbind errors", err)
	}
	p := uc.pm.GetByID(42)
	if repo.saved() != 1 || p == nil || p.GetSession() != sess {
		t.Fatalf("failed drain state: saves=%d player=%v", repo.saved(), p)
	}
	if sess.unbindCount() != 1 {
		t.Fatalf("UnbindNode() calls = %d, want 1", sess.unbindCount())
	}
}

func whotDrainTestRoom() *conf.Room {
	return &conf.Room{
		Table:    &conf.Room_Table{TableNum: 1, ChairNum: 2},
		Game:     &conf.Room_Game{MinMoney: 100, MaxMoney: 1000, BaseMoney: 100},
		Robot:    &conf.Room_Robot{},
		LogCache: &conf.Room_LogCache{},
	}
}

func waitForBizTest(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for business condition")
		}
		time.Sleep(time.Millisecond)
	}
}

type connectionSession struct {
	uid          string
	bindingToken string
	unbindErr    error

	mu      sync.Mutex
	unbinds int
}

func (sess *connectionSession) UID() string                                 { return sess.uid }
func (sess *connectionSession) BindingToken() string                        { return sess.bindingToken }
func (*connectionSession) BindNode(context.Context) error                   { return nil }
func (*connectionSession) Push(context.Context, int32, proto.Message) error { return nil }

func (sess *connectionSession) UnbindNode(context.Context) error {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.unbinds++
	return sess.unbindErr
}

func (sess *connectionSession) unbindCount() int {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.unbinds
}

var _ player.Session = (*connectionSession)(nil)

type usecaseRepoStub struct {
	mu      sync.Mutex
	saveErr error
	saves   int
}

func (repo *usecaseRepoStub) SavePlayer(context.Context, *player.BaseData) error {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	repo.saves++
	return repo.saveErr
}

func (*usecaseRepoStub) LoadPlayer(context.Context, int64) (*player.BaseData, error) {
	return nil, ErrPlayerNotFound
}

func (repo *usecaseRepoStub) saved() int {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	return repo.saves
}

type concurrentLoginRepo struct {
	mu     sync.Mutex
	loads  int
	loaded chan struct{}
}

func newConcurrentLoginRepo() *concurrentLoginRepo {
	return &concurrentLoginRepo{loaded: make(chan struct{})}
}

func (*concurrentLoginRepo) SavePlayer(context.Context, *player.BaseData) error { return nil }

func (repo *concurrentLoginRepo) LoadPlayer(context.Context, int64) (*player.BaseData, error) {
	repo.mu.Lock()
	repo.loads++
	if repo.loads == 2 {
		close(repo.loaded)
	}
	repo.mu.Unlock()
	<-repo.loaded
	return nil, ErrPlayerNotFound
}
