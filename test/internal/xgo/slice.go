package xgo

import "math/rand/v2"

func SliceCopy[T any](values []T) []T {
	if values == nil {
		return nil
	}
	return append([]T(nil), values...)
}

func SliceShuffle[T any](values []T) {
	rand.Shuffle(len(values), func(i, j int) {
		values[i], values[j] = values[j], values[i]
	})
}

func SliceSubtract[T comparable](values, removed []T) []T {
	result := SliceCopy(values)
	for _, target := range removed {
		for index, value := range result {
			if value == target {
				result = append(result[:index], result[index+1:]...)
				break
			}
		}
	}
	return result
}
