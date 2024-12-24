package calc

import (
    "math/rand"
    "time"
)

func init() {
    rand.Seed(time.Now().UnixNano())
}

func SliceCopy(slice []int32) []int32 {
    dst := make([]int32, len(slice))
    copy(dst, slice)
    return dst
}

func SliceShuffle(slice []int32) []int32 {
    for i := len(slice) - 1; i > 0; i-- {
        j := rand.Intn(i + 1)
        slice[i], slice[j] = slice[j], slice[i]
    }
    return slice
}

func SliceDel(slice []int32, values ...int32) []int32 {
    if len(slice) == 0 || len(values) == 0 {
        return slice
    }

    for _, val := range values {
        slice = sliceDel(slice, val)
    }
    return slice
}

func sliceDel(slice []int32, val int32) []int32 {
    for k, v := range slice {
        if v == val {
            return append(append([]int32{}, slice[:k]...), slice[k+1:]...)
        }
    }
    return slice
}
