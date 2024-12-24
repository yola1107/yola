package mymap

import (
    "fmt"
    "testing"
    "time"
)

//[1 2 20 4 21 5] [19 9] [9 5 21 4 20 19 2 1]
func TestUniquePair(t *testing.T) {
    var (
        start = time.Now()

        history = map[int64]map[int32]bool{}
    )

    fmt.Printf("use time:%v/s, history:%+v\n", time.Since(start).Seconds(), history)

    if len(history[0]) <= 0 {
        history[0] = map[int32]bool{}
    }
    history[0][111] = true

    fmt.Printf("use time:%v/s, history:%+v\n", time.Since(start).Seconds(), history)
    return
}
