package data

import (
	"context"
	"fmt"
	"math"
	"strconv"

	"yola/test/whot/internal/biz"
	"yola/test/whot/internal/biz/player"
	"yola/test/whot/pkg/xredis"
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
	return fmt.Sprintf("account:user:%v", uid)
}

func (r *playerRepo) SavePlayer(ctx context.Context, base *player.BaseData) error {
	key := playerKey(base.UID)
	return r.data.redis.HSet(ctx, key, playerRedisFields(base)).Err()
}

func (r *playerRepo) LoadPlayer(ctx context.Context, uid int64) (*player.BaseData, error) {
	key := playerKey(uid)

	v, err := r.data.redis.Exists(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	if v == 0 {
		return nil, biz.ErrPlayerNotFound
	}

	values, err := r.data.redis.HMGet(ctx, key, playerFields...).Result()
	if err != nil {
		return nil, err
	}
	if len(values) != len(playerFields) {
		return nil, fmt.Errorf("decode Redis player: expected %d fields, got %d", len(playerFields), len(values))
	}
	return decodeBaseData(uid, values)
}

func decodeBaseData(expectedUID int64, values []any) (*player.BaseData, error) {
	fields := make(map[string]string, len(playerFields))
	for index, field := range playerFields {
		if index >= len(values) || values[index] == nil {
			return nil, fmt.Errorf("decode Redis field %q: value is missing", field)
		}
		switch value := values[index].(type) {
		case string:
			fields[field] = value
		case []byte:
			fields[field] = string(value)
		default:
			return nil, fmt.Errorf("decode Redis field %q: unexpected value type", field)
		}
	}

	uid, err := parseInt64Field(fields, xredis.PlayerUIDField)
	if err != nil {
		return nil, err
	}
	if uid <= 0 || uid != expectedUID {
		return nil, fmt.Errorf("decode Redis field %q: UID mismatch", xredis.PlayerUIDField)
	}
	vip, err := parseInt32Field(fields, xredis.PlayerVIPField)
	if err != nil {
		return nil, err
	}
	money, err := parseFloat64Field(fields, xredis.PlayerMoneyField)
	if err != nil {
		return nil, err
	}
	channelID, err := parseInt32Field(fields, xredis.PlayerChannelIDField)
	if err != nil {
		return nil, err
	}
	payTotal, err := parseFloat64Field(fields, xredis.PlayerPayTotalField)
	if err != nil {
		return nil, err
	}
	withdrawTotal, err := parseFloat64Field(fields, xredis.PlayerWithdrawTotalField)
	if err != nil {
		return nil, err
	}
	presentBet, err := parseFloat64Field(fields, xredis.PlayerPresentBetField)
	if err != nil {
		return nil, err
	}
	presentProfit, err := parseFloat64Field(fields, xredis.PlayerPresentProfitField)
	if err != nil {
		return nil, err
	}
	presentWinScore, err := parseFloat64Field(fields, xredis.PlayerPresentWinScoreField)
	if err != nil {
		return nil, err
	}
	presentBoard, err := parseInt32Field(fields, xredis.PlayerPresentBoardField)
	if err != nil {
		return nil, err
	}
	totalBoard, err := parseInt32Field(fields, xredis.PlayerTotalBoardField)
	if err != nil {
		return nil, err
	}
	totalEarn, err := parseFloat64Field(fields, xredis.PlayerTotalEarnField)
	if err != nil {
		return nil, err
	}
	totalConsume, err := parseFloat64Field(fields, xredis.PlayerTotalConsumeField)
	if err != nil {
		return nil, err
	}
	allTotalBoard, err := parseInt64Field(fields, xredis.PlayerAllTotalBoardField)
	if err != nil {
		return nil, err
	}
	return &player.BaseData{
		UID:             uid,
		VIP:             vip,
		NickName:        fields[xredis.PlayerNickNameField],
		Avatar:          fields[xredis.PlayerAvatarField],
		AvatarURL:       fields[xredis.PlayerAvatarURLField],
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

func parseInt32Field(fields map[string]string, field string) (int32, error) {
	value, err := strconv.ParseInt(fields[field], 10, 32)
	if err != nil {
		return 0, fmt.Errorf("decode Redis field %q: invalid int32", field)
	}
	return int32(value), nil
}

func parseInt64Field(fields map[string]string, field string) (int64, error) {
	value, err := strconv.ParseInt(fields[field], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("decode Redis field %q: invalid int64", field)
	}
	return value, nil
}

func parseFloat64Field(fields map[string]string, field string) (float64, error) {
	value, err := strconv.ParseFloat(fields[field], 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, fmt.Errorf("decode Redis field %q: invalid float64", field)
	}
	return value, nil
}

func playerRedisFields(b *player.BaseData) map[string]string {
	return map[string]string{
		xredis.PlayerUIDField:             strconv.FormatInt(b.UID, 10),
		xredis.PlayerVIPField:             strconv.FormatInt(int64(b.VIP), 10),
		xredis.PlayerNickNameField:        b.NickName,
		xredis.PlayerAvatarField:          b.Avatar,
		xredis.PlayerAvatarURLField:       b.AvatarURL,
		xredis.PlayerMoneyField:           strconv.FormatFloat(b.Money, 'f', 2, 64),
		xredis.PlayerChannelIDField:       strconv.FormatInt(int64(b.ChannelID), 10),
		xredis.PlayerPayTotalField:        strconv.FormatFloat(b.PayTotal, 'f', 2, 64),
		xredis.PlayerWithdrawTotalField:   strconv.FormatFloat(b.WithdrawTotal, 'f', 2, 64),
		xredis.PlayerPresentBetField:      strconv.FormatFloat(b.PresentBet, 'f', 2, 64),
		xredis.PlayerPresentProfitField:   strconv.FormatFloat(b.PresentProfit, 'f', 2, 64),
		xredis.PlayerPresentWinScoreField: strconv.FormatFloat(b.PresentWinScore, 'f', 2, 64),
		xredis.PlayerPresentBoardField:    strconv.FormatInt(int64(b.PresentBoard), 10),
		xredis.PlayerTotalBoardField:      strconv.FormatInt(int64(b.TotalBoard), 10),
		xredis.PlayerTotalEarnField:       strconv.FormatFloat(b.TotalEarn, 'f', 2, 64),
		xredis.PlayerTotalConsumeField:    strconv.FormatFloat(b.TotalConsume, 'f', 2, 64),
		xredis.PlayerAllTotalBoardField:   strconv.FormatInt(b.AllTotalBoard, 10),
	}
}
