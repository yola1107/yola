package data

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"

	"yola/test/ludo/internal/biz"
	"yola/test/ludo/internal/biz/player"
	"yola/test/ludo/pkg/xredis"
)

var playerFields = []string{
	xredis.PlayerUIDField,
	xredis.PlayerVIPField,
	xredis.PlayerNickNameField,
	xredis.PlayerAvatarField,
	xredis.PlayerAvatarURLField,
	xredis.PlayerMoneyField,
	xredis.PlayerChannelIDField,
	xredis.PlayerPayTotalField,
	xredis.PlayerWithdrawTotalField,
	xredis.PlayerPresentBetField,
	xredis.PlayerPresentProfitField,
	xredis.PlayerPresentWinScoreField,
	xredis.PlayerPresentBoardField,
	xredis.PlayerTotalBoardField,
	xredis.PlayerTotalEarnField,
	xredis.PlayerTotalConsumeField,
	xredis.PlayerAllTotalBoardField,
}

func playerKey(uid int64) string {
	return fmt.Sprintf("account:user:%d", uid)
}

func (r *playerRepo) SavePlayer(ctx context.Context, base *player.BaseData) error {
	if base == nil || base.UID <= 0 {
		return errors.New("invalid player data")
	}
	return r.redis.HSet(ctx, playerKey(base.UID), playerRedisFields(base)).Err()
}

func (r *playerRepo) LoadPlayer(ctx context.Context, uid int64) (*player.BaseData, error) {
	key := playerKey(uid)
	exists, err := r.redis.Exists(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, biz.ErrPlayerNotFound
	}

	values, err := r.redis.HMGet(ctx, key, playerFields...).Result()
	if err != nil {
		return nil, err
	}
	data, err := redisFieldMap(playerFields, values)
	if err != nil {
		return nil, err
	}
	return playerFromRedisData(uid, data)
}

func redisFieldMap(fields []string, values []any) (map[string]string, error) {
	if len(values) != len(fields) {
		return nil, fmt.Errorf("redis player field count mismatch: got %d want %d", len(values), len(fields))
	}
	data := make(map[string]string, len(fields))
	for index, field := range fields {
		if values[index] == nil {
			return nil, fmt.Errorf("redis player field %q is missing", field)
		}
		switch value := values[index].(type) {
		case string:
			data[field] = value
		case []byte:
			data[field] = string(value)
		default:
			return nil, fmt.Errorf("redis player field %q has unsupported type", field)
		}
	}
	return data, nil
}

func playerFromRedisData(uid int64, data map[string]string) (*player.BaseData, error) {
	reader := playerFieldReader{data: data}
	storedUID := reader.int64(xredis.PlayerUIDField)
	if reader.err != nil {
		return nil, reader.err
	}
	if storedUID != uid {
		return nil, fmt.Errorf("redis player field %q does not match key", xredis.PlayerUIDField)
	}

	base := &player.BaseData{
		UID:             uid,
		VIP:             reader.int32(xredis.PlayerVIPField),
		NickName:        data[xredis.PlayerNickNameField],
		Avatar:          data[xredis.PlayerAvatarField],
		AvatarURL:       data[xredis.PlayerAvatarURLField],
		Money:           reader.float64(xredis.PlayerMoneyField),
		ChannelID:       reader.int32(xredis.PlayerChannelIDField),
		PayTotal:        reader.float64(xredis.PlayerPayTotalField),
		WithdrawTotal:   reader.float64(xredis.PlayerWithdrawTotalField),
		PresentBet:      reader.float64(xredis.PlayerPresentBetField),
		PresentProfit:   reader.float64(xredis.PlayerPresentProfitField),
		PresentWinScore: reader.float64(xredis.PlayerPresentWinScoreField),
		PresentBoard:    reader.int32(xredis.PlayerPresentBoardField),
		TotalBoard:      reader.int32(xredis.PlayerTotalBoardField),
		TotalEarn:       reader.float64(xredis.PlayerTotalEarnField),
		TotalConsume:    reader.float64(xredis.PlayerTotalConsumeField),
		AllTotalBoard:   reader.int64(xredis.PlayerAllTotalBoardField),
	}
	if reader.err != nil {
		return nil, reader.err
	}
	return base, nil
}

// playerFieldReader 保留第一个数值字段错误，错误中只包含字段名。
type playerFieldReader struct {
	data map[string]string
	err  error
}

func (r *playerFieldReader) value(field string) (string, bool) {
	if r.err != nil {
		return "", false
	}
	value, ok := r.data[field]
	if !ok {
		r.err = fmt.Errorf("redis player field %q is missing", field)
	}
	return value, ok
}

func (r *playerFieldReader) int64(field string) int64 {
	value, ok := r.value(field)
	if !ok {
		return 0
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		r.err = fmt.Errorf("redis player field %q is invalid", field)
	}
	return parsed
}

func (r *playerFieldReader) int32(field string) int32 {
	value, ok := r.value(field)
	if !ok {
		return 0
	}
	parsed, err := strconv.ParseInt(value, 10, 32)
	if err != nil {
		r.err = fmt.Errorf("redis player field %q is invalid", field)
		return 0
	}
	return int32(parsed)
}

func (r *playerFieldReader) float64(field string) float64 {
	value, ok := r.value(field)
	if !ok {
		return 0
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		r.err = fmt.Errorf("redis player field %q is invalid", field)
	}
	return parsed
}

func playerRedisFields(base *player.BaseData) map[string]string {
	return map[string]string{
		xredis.PlayerUIDField:             strconv.FormatInt(base.UID, 10),
		xredis.PlayerVIPField:             strconv.FormatInt(int64(base.VIP), 10),
		xredis.PlayerNickNameField:        base.NickName,
		xredis.PlayerAvatarField:          base.Avatar,
		xredis.PlayerAvatarURLField:       base.AvatarURL,
		xredis.PlayerMoneyField:           strconv.FormatFloat(base.Money, 'f', -1, 64),
		xredis.PlayerChannelIDField:       strconv.FormatInt(int64(base.ChannelID), 10),
		xredis.PlayerPayTotalField:        strconv.FormatFloat(base.PayTotal, 'f', -1, 64),
		xredis.PlayerWithdrawTotalField:   strconv.FormatFloat(base.WithdrawTotal, 'f', -1, 64),
		xredis.PlayerPresentBetField:      strconv.FormatFloat(base.PresentBet, 'f', -1, 64),
		xredis.PlayerPresentProfitField:   strconv.FormatFloat(base.PresentProfit, 'f', -1, 64),
		xredis.PlayerPresentWinScoreField: strconv.FormatFloat(base.PresentWinScore, 'f', -1, 64),
		xredis.PlayerPresentBoardField:    strconv.FormatInt(int64(base.PresentBoard), 10),
		xredis.PlayerTotalBoardField:      strconv.FormatInt(int64(base.TotalBoard), 10),
		xredis.PlayerTotalEarnField:       strconv.FormatFloat(base.TotalEarn, 'f', -1, 64),
		xredis.PlayerTotalConsumeField:    strconv.FormatFloat(base.TotalConsume, 'f', -1, 64),
		xredis.PlayerAllTotalBoardField:   strconv.FormatInt(base.AllTotalBoard, 10),
	}
}
