package xgo

import (
	"math/rand/v2"
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
	return rand.Float64() < value
}

func RandFloat(minimum, maximum float64) float64 {
	if maximum <= minimum {
		return minimum
	}
	return minimum + rand.Float64()*(maximum-minimum)
}

func RandInt[T Integer](minimum, maximum T) T {
	if maximum <= minimum {
		return minimum
	}
	return minimum + rand.N(maximum-minimum)
}

func RandIntInclusive[T Integer](minimum, maximum T) T {
	if maximum < minimum {
		return minimum
	}
	return minimum + rand.N(maximum-minimum+1)
}
