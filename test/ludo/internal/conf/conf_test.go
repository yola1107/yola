package conf

import (
	"math"
	"testing"

	"yola/test/internal/zapslog"

	"github.com/go-kratos/kratos/v3/config"
	"github.com/go-kratos/kratos/v3/config/file"
)

func TestConfigFileValidates(t *testing.T) {
	c := config.New(config.WithSource(file.NewSource("../../configs")))
	t.Cleanup(func() { _ = c.Close() })
	if err := c.Load(); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	var bootstrap Bootstrap
	if err := c.Scan(&bootstrap); err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if err := bootstrap.ValidateConfig(); err != nil {
		t.Fatalf("ValidateConfig() error = %v", err)
	}
}

func TestBootstrapValidateConfigRejectsNaNFee(t *testing.T) {
	bootstrap := validBootstrap()
	bootstrap.Room.Game.MaxMoney = -1
	bootstrap.Room.Game.Fee = math.NaN()
	if err := bootstrap.ValidateConfig(); err == nil {
		t.Fatal("ValidateConfig() error = nil")
	}
}

func TestRoomValidateAll(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Room)
	}{
		{name: "missing table", change: func(room *Room) { room.Table = nil }},
		{name: "zero tables", change: func(room *Room) { room.Table.TableNum = 0 }},
		{name: "too many chairs", change: func(room *Room) { room.Table.ChairNum = 5 }},
		{name: "zero base money", change: func(room *Room) { room.Game.BaseMoney = 0 }},
		{name: "invalid fee", change: func(room *Room) { room.Game.Fee = 1.1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			room := validRoom()
			test.change(room)
			if err := room.ValidateAll(); err == nil {
				t.Fatal("ValidateAll() error = nil")
			}
		})
	}
}

func TestBootstrapValidateConfigMoneyLimits(t *testing.T) {
	bootstrap := validBootstrap()
	bootstrap.Room.Game.MinMoney = 10
	bootstrap.Room.Game.MaxMoney = 50
	if err := bootstrap.ValidateConfig(); err == nil {
		t.Fatal("ValidateConfig() error = nil")
	}

	bootstrap.Room.Game.MaxMoney = -1
	if err := bootstrap.ValidateConfig(); err != nil {
		t.Fatalf("ValidateConfig() error = %v", err)
	}
}

func validRoom() *Room {
	return &Room{
		Table: &Room_Table{TableNum: 1, ChairNum: 4},
		Game:  &Room_Game{BaseMoney: 100, Fee: 0.2},
		Robot: &Room_Robot{},
	}
}

func validBootstrap() *Bootstrap {
	return &Bootstrap{
		Server: &Server{Grpc: &Server_GRPC{}},
		Data: &Data{
			Redis:    &Data_Redis{},
			Registry: &Data_Registry{},
		},
		Room: validRoom(),
		Log: &zapslog.Log{
			Format: "console",
			Output: "stdout",
		},
	}
}
