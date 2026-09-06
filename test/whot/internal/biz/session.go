package biz

import (
	"errors"
	"strconv"

	"yola/test/whot/internal/biz/player"
)

var ErrInvalidSessionUID = errors.New("invalid canonical session UID")

func SessionUID(sess player.Session) (int64, error) {
	if sess == nil {
		return 0, ErrInvalidSessionUID
	}
	raw := sess.UID()
	uid, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || uid <= 0 || strconv.FormatInt(uid, 10) != raw {
		return 0, ErrInvalidSessionUID
	}
	return uid, nil
}
