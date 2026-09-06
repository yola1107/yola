package table

import "yola/test/whot/internal/biz/player"

type Repo interface {
	LogoutGame(p *player.Player, code int32, msg string) error
}
