package conf

import "errors"

func (x *Bootstrap) ValidateConfig() error {
	if x == nil {
		return errors.New("bootstrap config is required")
	}
	if x.Server == nil || x.Server.Grpc == nil {
		return errors.New("server.grpc config is required")
	}
	if x.Data == nil {
		return errors.New("data config is required")
	}
	if x.Data.Redis == nil {
		return errors.New("data.redis config is required")
	}
	if x.Data.Registry == nil {
		return errors.New("data.registry config is required")
	}
	if x.Room == nil {
		return errors.New("room config is required")
	}
	if x.Room.Table == nil || x.Room.Table.TableNum <= 0 || x.Room.Table.ChairNum <= 0 || x.Room.Table.ChairNum > 4 {
		return errors.New("room.table config requires positive tables and 1 to 4 chairs")
	}
	if x.Room.Game == nil {
		return errors.New("room.game config is required")
	}
	if x.Room.Robot == nil {
		return errors.New("room.robot config is required")
	}
	return nil
}
