package dfs

import (
    "log"
    "sort"
    "testing"
    "time"
)

//[1 2 20 4 21 5] [19 9] [9 5 21 4 20 19 2 1]
func TestUniquePair(t *testing.T) {
    var (
        start = time.Now()
        //arr   = []int32{-8, -4, -3, 0, 1, 2, 4, 5, 8, 9}
        //arr = []int32{22, 1, 3, 4, 4, 5, 9, 17, 23}
        arr = []int32{20, 5, 19, 1, 2, 4, 9, 4, 21}
    )

    ret, ret2 := uniquePair(arr)

    log.Printf("use time:%v/s, cnt:%+v %+v %+v", time.Since(start).Seconds(), len(ret), ret, ret2)
    return
}

func uniquePair(arr []int32) (ret []int32, ret2 []int32) {
    if len(arr) < 2 {
        return
    }
    sort.SliceStable(arr, func(i, j int) bool {
        return arr[i] > arr[j]
    })
    sort.SliceStable(arr, func(i, j int) bool {
        return arr[i]&0x0f > arr[j]&0x0f
    })
    log.Printf("%+v", arr)

    i := 0
    for i < len(arr)-1 {
        if equal(arr[i], arr[i+1]) {
            ret = append(ret, arr[i], arr[i+1])
            i = i + 2
        } else {
            i++
            ret2 = append(ret2, arr[i])
        }
    }
    return
}

func equal(i, j int32) bool {
    if i-j == 1 || i-j == -1 {
        return true
    }
    if i&0x0f == j&0x0f {
        return true
    }
    return false
}

func TestRise(t *testing.T) {
    var (
        start = time.Now()
        arr   = []int32{20, 5, 19, 1, 25, 4, 9, 4, 21}
    )
    log.Printf("%+v", arr)

    ret := rise(arr)

    log.Printf("use time:%v/s, cnt:%+v %+v ", time.Since(start).Seconds(), len(ret), ret)
    return
}

func rise(arr []int32) []int32 {
    sort.SliceStable(arr, func(i, j int) bool {
        return arr[i] < arr[j]
    })
    sort.SliceStable(arr, func(i, j int) bool {
        return arr[i]&0x0f < arr[j]&0x0f
    })
    return arr
}
