package codes

const (
	Success     int32 = iota // 0
	Fail                     // 1
	RoomClosed               // 2
	KickByError              // 3
	KickByBroke              // 4
	KickByPasswordError
	MoneyOverMaxLimit
	MoneyBelowMinLimit
	MoneyBelowBaseLimit
	VIPLimit
	TokenFail
	SessionNotFound
	PlayerNotFound
	TableNotFound
	SwitchTable
	NotEnoughTable
	ExitTableFail
	EnterTableFail
	CreatePlayerFail
	PlayerAlreadyInTable
	PlayerInvalid
	NoTableSpecified // 找不到指定的桌子
	TableNoSpace     // 指定的桌子满员了
)
