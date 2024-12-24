package utils

import (
    "log"

    "github.com/fsnotify/fsnotify"
)

type (

    // 游戏配置
    GameCfg struct {
        local LocalCfg //本地配置
        cache CacheCfg //缓存配置
    }

    LocalCfg struct {
        Self string `json:"self"`
        Game struct {
            Port int `json:"port"`
        } `json:"game"`

        Tables   TableCfg `json:"tables"`
        RobotCfg RobotCfg `json:"robot_cfg"` // 机器人配置
    }

    // 缓存内的配置
    CacheCfg struct {
        LimitBonus float64 `json:"limit_bonus"` //不转化 bonus => bmoney
        TaxRate    float64 // an税
    }

    TableCfg struct {
        Vid              int     `json:"vid"`
        Zid              int     `json:"zid"`
        Begin            int     `json:"begin"`              // 桌子开始ID
        End              int     `json:"end"`                // 桌子结束ID
        IsNewbie         bool    `json:"is_newbie"`          // 是否新手场
        SeatCount        int     `json:"seat_count"`         // 椅子数量
        MinMoney         float64 `json:"min_money"`          // 最小投注
        MaxMoney         float64 `json:"max_money"`          // 最大投注
        BaseMoney        float64 `json:"base_money"`         // 底分
        AutoReady        bool    `json:"auto_ready"`         // 是否自动准备
        Chlimit          float64 `json:"ch_limit"`           // 个人最高投注上限
        PotLimit         float64 `json:"pot_limit"`          // 所有人最高投注上限
        SeeRound         int     `json:"see_round"`          // 最小看牌回合数
        AutoSeeRound     int     `json:"auto_see_round"`     // 自动看牌回合数
        Fee              float64 `json:"fee"`                // 抽水比例
        SpeakerMinMoney  float64 `json:"speaker_minmoney"`   // 大奖播报最小的钱
        SpeakerCardType  int     `json:"speaker_card_type"`  // 大奖播报最小牌型
        HighStrengthRate int     `json:"high_strength_rate"` // 高牌的概率
    }

    // 机器人配置
    RobotCfg struct {
        IsOpen        bool    `json:"is_open"`         // 是否开启机器人
        Num           int     `json:"num"`             // 机器人数量
        TableMaxCount int     `json:"table_max_count"` // 桌子上机器人最多的数量
        MinPlayCount  int     `json:"min_play_count"`  // 最小的机器人游戏数量
        BeginId       int64   `json:"id_begin"`        // 机器人开始id
        MinMoney      float64 `json:"min_money"`       // 机器人带入最小金币
        MaxMoney      float64 `json:"max_money"`
        StandMinMoney float64 `json:"stand_min_money"` // 机器人站起的最小金币
        StandMaxMoney float64 `json:"stand_max_money"`
    }
)

func loadGameConf() {
    watcher, err := fsnotify.NewWatcher()
    if err != nil {
        log.Fatal(err)
    }
    defer watcher.Close()

    // Start listening for events.
    go func() {
        for {
            select {
            case event, ok := <-watcher.Events:
                if !ok {
                    return
                }
                log.Println("event:", event)
                if event.Has(fsnotify.Write) {
                    log.Println("modified file:", event.Name)
                }
            case err, ok := <-watcher.Errors:
                if !ok {
                    return
                }
                log.Println("error:", err)
            }
        }
    }()

    // Add a path.
    err = watcher.Add("/tmp")
    if err != nil {
        log.Fatal(err)
    }

    // Block main goroutine forever.
    <-make(chan struct{})
}
