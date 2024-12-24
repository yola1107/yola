package calc

const (
	AcMove     = 0x0000 //移动
	AcEat      = 0x0001 //吃子
	AcKilling  = 0x0010 //将军
	AcMustKill = 0x0100 //绝杀
	AcKill     = 0x1000 //
)

type Step struct {
	moveId  int32
	killId  int32
	fromRow int32
	fromCol int32
	toRow   int32
	toCol   int32
	action  int32
	canEat  map[int32]Stone //key: nId
}

func (s *Step) GetMoveId() int32 {
	return s.moveId
}
func (s *Step) GetKillId() int32 {
	return s.killId
}
func (s *Step) GetFromRow() int32 {
	return s.fromRow
}
func (s *Step) GetFromCol() int32 {
	return s.fromCol
}
func (s *Step) GetToRow() int32 {
	return s.toRow
}
func (s *Step) GetToCol() int32 {
	return s.toCol
}
func (s *Step) GetAction() int32 {
	return s.action
}
func (s *Step) GetCanEat() map[int32]Stone {
	return s.canEat
}
