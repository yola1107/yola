package table

import (
	"testing"

	"yola/test/internal/pushbench"
	"yola/test/ludo/api/v1"
	"yola/test/ludo/internal/biz/player"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func BenchmarkTablePush(b *testing.B) {
	pushbench.Run(b, "ludo", func(pusher pushbench.Pusher, tableCount int) func(int) {
		fanout := newPushFanout(pusher, tableCount)
		message := wrapperspb.Bytes(make([]byte, 256))
		return func(index int) { fanout(index, message) }
	})
}

func BenchmarkTableCadence(b *testing.B) { pushbench.RunCadence(b, "ludo", newPushFanout) }

func TestTablePushSocketOrdering(t *testing.T) { pushbench.RunSocketOrdering(t, "ludo", newPushFanout) }

func newPushFanout(pusher pushbench.Pusher, tableCount int) func(int, proto.Message) {
	manager := &Manager{pusher: pusher}
	tables := make([]*Table, tableCount)
	for index := range tables {
		gameTable := &Table{ID: int32(index + 1), manager: manager, maxPlayers: 4, seats: make([]*player.Player, 4)}
		for chair := range gameTable.seats {
			uid := int64(index*4 + chair + 1)
			p := player.New(&player.Raw{ID: uid, BaseData: &player.BaseData{UID: uid}})
			p.SetTableID(gameTable.ID)
			p.SetChairID(int32(chair))
			gameTable.seats[chair] = p
		}
		tables[index] = gameTable
	}
	return func(index int, message proto.Message) { tables[index].sendToAll(v1.GameCommand(1), message) }
}
