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
	return r.data.redis.HSet(ctx, playerKey(base.UID), playerRedisFields(base)).Err()
}

func (r *playerRepo) LoadPlayer(ctx context.Context, uid int64) (*player.BaseData, error) {
	key := playerKey(uid)
	exists, err := r.data.redis.Exists(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, biz.ErrPlayerNotFound
	}

	values, err := r.data.redis.HMGet(ctx, key, playerFields...).Result()
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
	storedUID, err := parseInt64Field(data, xredis.PlayerUIDField)
	if err != nil {
		return nil, err
	}
	if storedUID != uid {
		return nil, fmt.Errorf("redis player field %q does not match key", xredis.PlayerUIDField)
	}
	vip, err := parseInt32Field(data, xredis.PlayerVIPField)
	if err != nil {
		return nil, err
	}
	money, err := parseFloat64Field(data, xredis.PlayerMoneyField)
	if err != nil {
		return nil, err
	}
	channelID, err := parseInt32Field(data, xredis.PlayerChannelIDField)
	if err != nil {
		return nil, err
	}
	payTotal, err := parseFloat64Field(data, xredis.PlayerPayTotalField)
	if err != nil {
		return nil, err
	}
	withdrawTotal, err := parseFloat64Field(data, xredis.PlayerWithdrawTotalField)
	if err != nil {
		return nil, err
	}
	presentBet, err := parseFloat64Field(data, xredis.PlayerPresentBetField)
	if err != nil {
		return nil, err
	}
	presentProfit, err := parseFloat64Field(data, xredis.PlayerPresentProfitField)
	if err != nil {
		return nil, err
	}
	presentWinScore, err := parseFloat64Field(data, xredis.PlayerPresentWinScoreField)
	if err != nil {
		return nil, err
	}
	presentBoard, err := parseInt32Field(data, xredis.PlayerPresentBoardField)
	if err != nil {
		return nil, err
	}
	totalBoard, err := parseInt32Field(data, xredis.PlayerTotalBoardField)
	if err != nil {
		return nil, err
	}
	totalEarn, err := parseFloat64Field(data, xredis.PlayerTotalEarnField)
	if err != nil {
		return nil, err
	}
	totalConsume, err := parseFloat64Field(data, xredis.PlayerTotalConsumeField)
	if err != nil {
		return nil, err
	}
	allTotalBoard, err := parseInt64Field(data, xredis.PlayerAllTotalBoardField)
	if err != nil {
		return nil, err
	}
	return &player.BaseData{
		UID:             uid,
		VIP:             vip,
		NickName:        data[xredis.PlayerNickNameField],
		Avatar:          data[xredis.PlayerAvatarField],
		AvatarURL:       data[xredis.PlayerAvatarURLField],
		Money:           money,
		ChannelID:       channelID,
		PayTotal:        payTotal,
		WithdrawTotal:   withdrawTotal,
		PresentBet:      presentBet,
		PresentProfit:   presentProfit,
		PresentWinScore: presentWinScore,
		PresentBoard:    presentBoard,
		TotalBoard:      totalBoard,
		TotalEarn:       totalEarn,
		TotalConsume:    totalConsume,
		AllTotalBoard:   allTotalBoard,
	}, nil
}

func parseInt64Field(data map[string]string, field string) (int64, error) {
	value, ok := data[field]
	if !ok {
		return 0, fmt.Errorf("redis player field %q is missing", field)
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("redis player field %q is invalid", field)
	}
	return parsed, nil
}

func parseInt32Field(data map[string]string, field string) (int32, error) {
	value, ok := data[field]
	if !ok {
		return 0, fmt.Errorf("redis player field %q is missing", field)
	}
	parsed, err := strconv.ParseInt(value, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("redis player field %q is invalid", field)
	}
	return int32(parsed), nil
}

func parseFloat64Field(data map[string]string, field string) (float64, error) {
	value, ok := data[field]
	if !ok {
		return 0, fmt.Errorf("redis player field %q is missing", field)
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return 0, fmt.Errorf("redis player field %q is invalid", field)
	}
	return parsed, nil
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
