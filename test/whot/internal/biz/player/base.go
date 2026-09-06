package player

// BaseData PlayerBaseData 结构体
type BaseData struct {
	UID             int64 // 用户ID
	VIP             int32 // VIP等级
	NickName        string
	Avatar          string
	AvatarURL       string
	Money           float64 // 金币
	ChannelID       int32   // 渠道ID
	PayTotal        float64 // 总充值金额
	WithdrawTotal   float64 // 玩家提现总金额
	PresentBet      float64 // 本次游戏中的所有下注
	PresentProfit   float64 // 本次游戏中的纯盈利
	PresentWinScore float64 // 本次游戏中的总赢
	PresentBoard    int32   // 局数
	TotalBoard      int32   // 此游戏的总局数
	TotalEarn       float64 // 此游戏的总赢
	TotalConsume    float64 // 此游戏的总消耗
	AllTotalBoard   int64   // 玩家游戏局数
}

func (p *Player) UseMoney(money float64) {
	p.baseData.Money -= money
}

func (p *Player) AddMoney(money float64) {
	p.baseData.Money += money
}

func (p *Player) GetAllMoney() float64 {
	return p.baseData.Money
}

func (p *Player) GetVipGrade() int32 {
	return p.baseData.VIP
}

func (p *Player) GetPlayerID() int64 {
	return p.baseData.UID
}

func (p *Player) GetNickName() string {
	return p.baseData.NickName
}

func (p *Player) GetAvatar() string {
	return p.baseData.Avatar
}

func (p *Player) GetAvatarURL() string {
	return p.baseData.AvatarURL
}
