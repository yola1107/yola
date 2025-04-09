package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestRedis(t *testing.T) {
	// 创建 Redis 客户端
	rdb := redis.NewClient(&redis.Options{
		Addr: "192.168.56.131:6379", // Redis 地址
	})

	fmt.Printf("%+v\n", GetGameRTPConfig(context.Background(), rdb))
}

type (
	// GameRTPConfig 游戏RTP配置数据统计
	GameRTPConfig struct {
		RTP       float64               `json:"rtp"`       // 目标RTP %
		Gas       float64               `json:"gas"`       // 费率，BET只计算95%
		Bility    float64               `json:"bility"`    // 输赢概率 %
		Factor    float64               `json:"factor"`    // 波动因子 %
		TeenPatti map[string][]CardType `json:"teenpatti"` // 牌型权重
	}
	CardType struct {
		Type   int    `json:"type"` // 1: 豹子 2:顺金 3:顺子 4:金花 5:对子 6:单牌
		Weight int32  `json:"weight"`
		Desc   string `json:"desc"`
	}
)

var (
	// 默认配置
	_default = GameRTPConfig{
		RTP:    0.97,
		Gas:    0.95,
		Bility: 0.3,
		Factor: 0.08,
		TeenPatti: map[string][]CardType{
			// A 类配牌
			"A": {
				{Type: 1, Weight: 1, Desc: "豹子(1)"},
				//{Type: 2, Weight: 27, Desc: "顺金(2)"},
				{Type: 3, Weight: 2, Desc: "顺子(3)"},
				{Type: 4, Weight: 30, Desc: "金花(4)"},
				{Type: 5, Weight: 10, Desc: "对子(5)"},
				{Type: 6, Weight: 30, Desc: "单牌(6)"},
			},
			// B 类配牌
			"B": {
				{Type: 1, Weight: 5, Desc: "豹子(1)"},
				{Type: 2, Weight: 15, Desc: "顺金(2)"},
				{Type: 3, Weight: 80, Desc: "顺子(3)"},
				{Type: 4, Weight: 0, Desc: "金花(4)"},
				{Type: 5, Weight: 0, Desc: "对子(5)"},
				{Type: 6, Weight: 0, Desc: "单牌(6)"},
			},
		},
	}
)

func (m *GameRTPConfig) MarshalBinary() (data []byte, err error) { return json.Marshal(m) }
func (m *GameRTPConfig) UnmarshalBinary(data []byte) error       { return json.Unmarshal(data, m) }

func GetGameRTPConfig(ctx context.Context, client *redis.Client) GameRTPConfig {
	c := GameRTPConfig{}

	key := "config1"
	field := "data"

	if err := client.HGet(ctx, key, field).Scan(&c); err != nil {

		if errors.Is(err, redis.Nil) {
			err = client.HSet(ctx, key, field, &_default).Err()
		}
		if err != nil {
			fmt.Printf("GetGameWater. key=%+v err:%v", key, err)
		}
		return _default
	}
	return c
}

func TestRandWeight(t *testing.T) {
	//// 创建 Redis 客户端
	//rdb := redis.NewClient(&redis.Options{
	//	Addr: "192.168.56.131:6379", // Redis 地址
	//})
	//
	//fmt.Printf("%+v\n", GetGameRTPConfig(context.Background(), rdb))

	m := map[int]int{}
	total := 0
	for i := 0; i < 100; i++ {
		x := randWeighted(_default.TeenPatti["A"])
		m[x.Type]++
		total++
	}

	fmt.Printf("total:%+v %+v\n", total, m)

}

// 权重
func randWeighted(weighted []CardType) CardType {
	total := int32(0)
	ats := []int32{}
	for _, v := range weighted {
		total += v.Weight
		ats = append(ats, total)
	}
	rnd := rand.Int() % int(total) //. RandInt(0, total)
	for i, v := range ats {
		if int32(rnd) < v {
			return weighted[i]
		}
	}
	return CardType{}
}

func TestExpire(t *testing.T) {
	for i := 0; i < 10; i++ {
		fmt.Printf("%+v\n", calcExpireIn())
		time.Sleep(time.Second * 5)
	}
}

// 计算下一个凌晨5点的时间差
func calcExpireIn() time.Duration {
	// 获取当前时间
	now := time.Now()

	// 获取今天凌晨5点的时间
	nextFiveAM := getNextFiveAM(now)

	// 如果当前时间已经过了0点，设置为明天凌晨0点
	if now.After(nextFiveAM) {
		// 加上24小时，指向明天的凌晨5点
		nextFiveAM = nextFiveAM.Add(24 * time.Hour)
	}

	// 计算时间差
	expireIn := nextFiveAM.Sub(now)
	return expireIn
}

// 获取今天的凌晨5点
func getNextFiveAM(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

func TestExpire2(t *testing.T) {
	for i := 0; i < 10; i++ {
		fmt.Printf("%+v\n", nextMonday5AM())
		time.Sleep(time.Second * 5)
	}
}

// 计算下一个周一凌晨5点的时间戳（秒）
func nextMonday5AM() time.Duration {
	now := time.Now()
	// 当前时间是星期几
	weekday := int(now.Weekday())
	// 如果是周日，weekday = 0，周一则是 1
	daysUntilMonday := (7 - weekday + 1) % 7
	// 距离下一个周一的时间
	nextMonday := now.AddDate(0, 0, daysUntilMonday)
	// 计算下周一的凌晨 5 点
	nextMonday5AM := time.Date(nextMonday.Year(), nextMonday.Month(), nextMonday.Day(), 5, 0, 0, 0, time.Local)
	return time.Until(nextMonday5AM)
}

func TestExpire3(t *testing.T) {
	for i := 0; i < 10; i++ {
		fmt.Printf("%+v\n", getStartOfWeekTimestamp())
		fmt.Printf("%+v\n", getDayBetKey())
		time.Sleep(time.Second * 5)
	}
}

// 获取本周的开始时间戳（周一 0:00 AM）
func getStartOfWeekTimestamp() string {
	// 获取当前时间
	currentTime := time.Now()

	// 计算当前时间是周几
	weekday := currentTime.Weekday()

	// 计算本周周一的时间
	daysSinceMonday := (int(weekday) + 6) % 7
	weekStart := currentTime.AddDate(0, 0, -daysSinceMonday) // 当前周的周一
	// 设置时间为周一的 5 点
	weekStartAt5AM := time.Date(weekStart.Year(), weekStart.Month(), weekStart.Day(), 0, 0, 0, 0, weekStart.Location())

	return weekStartAt5AM.Format("20060102")
	//return weekStartAt5AM.Format("20060102150405")
}

// 获取当天的bet的 Redis 键      rank:day:20250113050000
func getDayBetKey() string {
	return fmt.Sprintf("rank:day:bet:%v", time.Now().Format("20060102"))
	//return fmt.Sprintf("rank:day:bet:%v", getStartOfDayTimestamp())
}
