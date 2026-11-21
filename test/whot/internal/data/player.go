package data

import (
	"context"
	"fmt"
	"math"
	"strconv"

	"yola/test/whot/internal/biz"
	"yola/test/whot/internal/biz/player"
	"yola/test/whot/pkg/xredis"

	"github.com/redis/go-redis/v9"
)

type playerRepo struct {
	redis redis.UniversalClient
}

// NewPlayerRepo 使用调用方管理的 Redis 连接读写玩家档案。
func NewPlayerRepo(client redis.UniversalClient) biz.PlayerRepo {
	return &playerRepo{redis: client}
}

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
	if len(values) != len(playerFields) {
		return nil, fmt.Errorf("decode Redis player: expected %d fields, got %d", len(playerFields), len(values))
	}
	return decodeBaseData(uid, values)
}

func decodeBaseData(expectedUID int64, values []any) (*player.BaseData, error) {
	fields, err := decodePlayerFields(values)
	if err != nil {
		return nil, err
	}

	decoder := playerFieldDecoder{fields: fields}
	uid := decoder.int64(xredis.PlayerUIDField)
	if decoder.err != nil {
		return nil, decoder.err
	}
	if uid <= 0 || uid != expectedUID {
		return nil, fmt.Errorf("decode Redis field %q: UID mismatch", xredis.PlayerUIDField)
	}

	base := &player.BaseData{
		UID:             uid,
		VIP:             decoder.int32(xredis.PlayerVIPField),
		NickName:        fields[xredis.PlayerNickNameField],
		Avatar:          fields[xredis.PlayerAvatarField],
		AvatarURL:       fields[xredis.PlayerAvatarURLField],
		Money:           decoder.float64(xredis.PlayerMoneyField),
		ChannelID:       decoder.int32(xredis.PlayerChannelIDField),
		PayTotal:        decoder.float64(xredis.PlayerPayTotalField),
		WithdrawTotal:   decoder.float64(xredis.PlayerWithdrawTotalField),
		PresentBet:      decoder.float64(xredis.PlayerPresentBetField),
		PresentProfit:   decoder.float64(xredis.PlayerPresentProfitField),
		PresentWinScore: decoder.float64(xredis.PlayerPresentWinScoreField),
		PresentBoard:    decoder.int32(xredis.PlayerPresentBoardField),
		TotalBoard:      decoder.int32(xredis.PlayerTotalBoardField),
		TotalEarn:       decoder.float64(xredis.PlayerTotalEarnField),
		TotalConsume:    decoder.float64(xredis.PlayerTotalConsumeField),
		AllTotalBoard:   decoder.int64(xredis.PlayerAllTotalBoardField),
	}
	if decoder.err != nil {
		return nil, decoder.err
	}
	return base, nil
}

func decodePlayerFields(values []any) (map[string]string, error) {
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

	return fields, nil
}

// playerFieldDecoder 保留首个数值字段错误。
type playerFieldDecoder struct {
	fields map[string]string
	err    error
}

func (d *playerFieldDecoder) int32(field string) int32 {
	if d.err != nil {
		return 0
	}
	value, err := strconv.ParseInt(d.fields[field], 10, 32)
	if err != nil {
		d.err = fmt.Errorf("decode Redis field %q: invalid int32", field)
		return 0
	}
	return int32(value)
}

func (d *playerFieldDecoder) int64(field string) int64 {
	if d.err != nil {
		return 0
	}
	value, err := strconv.ParseInt(d.fields[field], 10, 64)
	if err != nil {
		d.err = fmt.Errorf("decode Redis field %q: invalid int64", field)
		return 0
	}
	return value
}

func (d *playerFieldDecoder) float64(field string) float64 {
	if d.err != nil {
		return 0
	}
	value, err := strconv.ParseFloat(d.fields[field], 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		d.err = fmt.Errorf("decode Redis field %q: invalid float64", field)
		return 0
	}
	return value
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
