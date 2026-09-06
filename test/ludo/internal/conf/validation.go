package conf

import (
	"errors"
	"math"
)

func (x *Bootstrap) ValidateConfig() error {
	if err := x.ValidateAll(); err != nil {
		return err
	}
	game := x.Room.Game
	if math.IsNaN(game.Fee) || math.IsInf(game.Fee, 0) {
		return errors.New("fee must be finite")
	}
	if game.MaxMoney != -1 && (game.MaxMoney < game.MinMoney || game.MaxMoney < game.BaseMoney) {
		return errors.New("maximum money must cover minimum and base money")
	}
	return nil
}
