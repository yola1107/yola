package dfs

import (
    "fmt"
    "log"
    "testing"
    "time"
)

func TestPermute(t *testing.T) {
    var (
        start   = time.Now()
        _rows   = []int32(nil)
        _dices  = []int32{3, 4} //2个色子
        _maxCnt = 2             //15个chess
    )

    for i := int32(1); i <= int32(_maxCnt); i++ {
        _rows = append(_rows, i)
    }

    ret := Permute(_rows, _dices)

    str := "\n"
    for _, v := range ret {
        if len(v) == 2 {
            str += fmt.Sprintf("{Row:%d->%d}\n", v[0], v[1])
        } else if len(v) == 4 {
            str += fmt.Sprintf("{Row:%d->%d, Row:%d->%d}\n", v[0], v[1], v[2], v[3])
        } else {
            log.Fatalf("====> bad step, %+v", v)
        }
    }

    log.Printf("use time: %v, _rows:%+v _dices:%+v cnt:%+v %+v", time.Since(start).Milliseconds(), _rows, _dices, len(ret), str)
    return
}

func Permute(_rows []int32, _dices []int32) [][]int32 {
    if len(_rows) == 0 {
        return nil
    }
    if len(_dices) == 0 {
        return nil
    }
    var res [][]int32
    var tmp []int32
    var visited = make([]bool, len(_dices))
    backtracking(_rows, _dices, &res, tmp, visited)
    return res
}

func backtracking(_rows []int32, _dices []int32, res *[][]int32, tmp []int32, visited []bool) {
    // 成功找到一组
    if len(tmp) == len(_dices) || len(tmp)/2 == len(_dices) || (len(tmp)/4 == len(_dices) && _dices[0] == _dices[1]) {
        var c = make([]int32, len(tmp))
        copy(c, tmp)
        *res = append(*res, c)
        if len(tmp)/4 == len(_dices) && _dices[0] == _dices[1] {
            return
        }
        if len(tmp)/2 == len(_dices) {
            return
        }
    }

    // 回溯
    for j := 0; j < len(_dices); j++ {

        for i := 0; i < len(_rows); i++ {

            if !visited[j] {
                visited[j] = true

                tmp = append(tmp, _rows[i], _dices[j])
                backtracking(_rows, _dices, res, tmp, visited)
                tmp = tmp[:len(tmp)-2]

                visited[j] = false

            }

        }
    }
}
