package xgo

func SliceCopy[T any](values []T) []T {
	return append([]T(nil), values...)
}

func SliceShuffle[T any](values []T) {
	for i := len(values) - 1; i > 0; i-- {
		j := RandIntInclusive(0, i)
		values[i], values[j] = values[j], values[i]
	}
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
