package dfs

import (
    "log"
    "testing"
    "time"
)

func TestOrder(t *testing.T) {
    var (
        start = time.Now()
        arr   = []int32{1, 2, 3, 4}
    )

    ret := Order(arr)

    log.Printf("use time:%v, cnt:%+v %+v", time.Since(start).Milliseconds(), len(ret), ret)
    return
}

func Order(arr []int32) [][]int32 {
    if len(arr) == 0 {
        return nil
    }
    var res [][]int32
    var tmp []int32
    var visited = make([]bool, len(arr))
    order(arr, &res, tmp, 0, visited)
    return res
}

func order(arr []int32, res *[][]int32, cache []int32, start int, visited []bool) {
    // 成功找到一组
    if len(cache) == 2 {
        var c = make([]int32, len(cache))
        copy(c, cache)
        *res = append(*res, c)
        return
    }

    // 回溯
    for i := start; i < len(arr); i++ {
        if visited[i] {
            continue
        }

        visited[i] = true
        cache = append(cache, arr[i])
        order(arr, res, cache, i+1, visited)
        cache = cache[:len(cache)-1]
        visited[i] = false
    }
}
