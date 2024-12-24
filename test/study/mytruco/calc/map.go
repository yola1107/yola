package calc

type Map struct {
    cardObj   GameCards  //牌信息
    circleObj GameCircle //圈(小局)
    steps     []*Step    //步骤
    active    int        //活动玩家
}

func NewMap() *Map {
    m := &Map{}
    return m
}

func (m *Map) CanOp(chair int, step *Step) (can bool) {
    return
}

func (m *Map) Op(chair int, op *Step) {

}
