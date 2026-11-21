package table

import "testing"

func BenchmarkChooseBestCard(b *testing.B) {
	for _, sample := range []struct {
		name       string
		hand       []int32
		candidates []int32
	}{
		{"five", []int32{105, 110, 205, 311, WhotCard}, []int32{105, 110, 205, WhotCard}},
		{"fifteen", []int32{105, 110, 205, 311, WhotCard, 101, 102, 103, 211, 310, 407, 408, 411, 503, 505}, []int32{105, 110, 205, WhotCard, 101, 102, 103, 505}},
	} {
		b.Run(sample.name, func(b *testing.B) {
			for b.Loop() {
				chooseBestCard(sample.candidates, sample.hand, 105, 2)
			}
		})
	}
}
