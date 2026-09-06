package table

import (
	"yola/test/ludo/internal/biz/player"
)

type Repo interface {
	LogoutGame(p *player.Player, code int32, message string) error
}
