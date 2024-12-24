package calc

type aiTest struct {
    chair int
    cMap  *Map
}

func NewAiTest(chair int, cMap *Map) *aiTest {
    return &aiTest{
        chair: chair,
        cMap:  cMap,
    }
}

//移动
func (a *aiTest) Move() *Step {
    step := a.bestMove()
    if step == nil {
        return nil
    }
    if can := a.cMap.CanOp(a.chair, step); can {
        return nil
    }
    a.cMap.Op(a.chair, step)
    return step
}

//获取最佳路径
func (a *aiTest) bestMove() *Step {
    var best *Step
    return best
}
