package xgo

import (
	"crypto/rand"
	"math/big"
)

type Integer interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64
}

func IsHitFloat(value float64) bool {
	if value <= 0 {
		return false
	}
	if value >= 1 {
		return true
	}
	return RandFloat(0, 1) < value
}

func RandFloat(minimum, maximum float64) float64 {
	if maximum <= minimum {
		return minimum
	}
	const steps = 1 << 53 // float64 的有效位数为 53。
	fraction := float64(RandInt[int64](0, steps)) / steps
	return minimum + fraction*(maximum-minimum)
}

func RandInt[T Integer](minimum, maximum T) T {
	if maximum <= minimum {
		return minimum
	}
	return RandIntInclusive(minimum, maximum-1)
}

func RandIntInclusive[T Integer](minimum, maximum T) T {
	if maximum <= minimum {
		return minimum
	}
	// 闭区间可能跨越整个 int64，先用大整数计算宽度再采样。
	lower := big.NewInt(int64(minimum))
	width := big.NewInt(int64(maximum))
	width.Sub(width, lower)
	width.Add(width, big.NewInt(1))
	n, err := rand.Int(rand.Reader, width)
	if err != nil {
		panic(err)
	}
	return T(n.Add(n, lower).Int64())
}
