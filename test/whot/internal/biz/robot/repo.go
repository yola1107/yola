package robot

import (
	"context"

	"yola/test/whot/internal/biz/player"
)

// Repo 抽象接口
type Repo interface {
	CreateRobot(raw *player.Raw) (*player.Player, error)
	EnterRobots(ctx context.Context, players []*player.Player) ([]int64, error)
}
