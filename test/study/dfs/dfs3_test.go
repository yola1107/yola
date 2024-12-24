package dfs

import (
    "fmt"
    "log"
    "testing"
)

func TestCombination(t *testing.T) {
    var (
        _arr    = []int32(nil)
        _target = int32(20)
    )
    for i := int32(1); i < 15; i++ {
        _arr = append(_arr, i)
    }
    ret := combination(_arr, _target)

    str := "\n"
    for _, v := range ret {
        str += fmt.Sprintf("%+v\n", v)
    }

    log.Printf("hello world, cnt:%d %+v \nret:%+v", len(ret), str, ret)
}

func combination(arr []int32, target int32) [][]int32 {
    if len(arr) == 0 {
        return nil
    }
    if target < 0 {
        return nil
    }
    var res [][]int32
    var cache []int32
    var visited = make([]bool, len(arr))
    back(arr, &res, target, cache, 0, visited)
    return res
}

func back(arr []int32, res *[][]int32, target int32, cache []int32, index int, visited []bool) {
    if target == toPoints(cache) {
        var c = make([]int32, len(cache))
        copy(c, cache)
        *res = append(*res, c)
        return
    }
    for i := index; i < len(arr); i++ {
        visited[i] = true
        cache = append(cache, arr[i]) //记录元素值
        back(arr, res, target, cache, i+1, visited)
        cache = cache[:len(cache)-1]
        visited[i] = false
    }
}

func toPoints(arr []int32) (sum int32) {
    for _, v := range arr {
        sum += toPoint(v)
    }
    return
}

func toPoint(x int32) int32 {
    return x //% 0x0f
}
