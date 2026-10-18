package player_test

import (
	"context"
	"sync"
	"testing"

	"yola/test/ludo/internal/biz/player"

	"google.golang.org/protobuf/proto"
)

type retainedSession struct{}

func (*retainedSession) UID() string                                      { return "42" }
func (*retainedSession) BindingToken() string                             { return "binding" }
func (*retainedSession) BindNode(context.Context) error                   { return nil }
func (*retainedSession) RenewNode(context.Context) error                  { return nil }
func (*retainedSession) UnbindNode(context.Context) error                 { return nil }
func (*retainedSession) Push(context.Context, int32, proto.Message) error { return nil }

func TestSessionCanBeReadDuringReconnect(t *testing.T) {
	sess := new(retainedSession)
	p := player.New(&player.Raw{Session: sess, BaseData: &player.BaseData{UID: 42}})
	var calls sync.WaitGroup
	calls.Add(2)
	go func() {
		defer calls.Done()
		for range 1000 {
			p.UpdateSession(nil)
			p.UpdateSession(sess)
		}
	}()
	go func() {
		defer calls.Done()
		for range 1000 {
			if current := p.GetSession(); current != nil && current != sess {
				t.Errorf("current session = %v, want reconnect session", current)
			}
		}
	}()
	calls.Wait()
}
