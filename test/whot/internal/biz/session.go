package biz

import (
	"errors"
	"strconv"

	"yola/test/whot/internal/biz/player"
)

var ErrInvalidSessionUID = errors.New("invalid canonical session UID")

func SessionUID(sess player.Session) (int64, error) {
	uid, ok := sessionUID(sess)
	if !ok {
		return 0, ErrInvalidSessionUID
	}
	return uid, nil
}

func sessionUID(sess player.Session) (int64, bool) {
	if sess == nil {
		return 0, false
	}
	raw := sess.UID()
	uid, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || uid <= 0 || strconv.FormatInt(uid, 10) != raw {
		return 0, false
	}
	return uid, true
}
