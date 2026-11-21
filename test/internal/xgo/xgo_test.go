package xgo

import (
	"reflect"
	"slices"
	"testing"
)

func TestRandomBounds(t *testing.T) {
	for range 1_000 {
		if value := RandInt(-3, 5); value < -3 || value >= 5 {
			t.Fatalf("RandInt() = %d outside [-3,5)", value)
		}
		if value := RandIntInclusive[int8](-100, 100); value < -100 || value > 100 {
			t.Fatalf("RandIntInclusive() = %d outside [-100,100]", value)
		}
		if value := RandFloat(0, 1); value < 0 || value >= 1 {
			t.Fatalf("RandFloat() = %g outside [0,1)", value)
		}
	}
	if value := RandInt[int64](-1<<62, 1<<62); value < -1<<62 || value >= 1<<62 {
		t.Fatalf("RandInt() = %d outside [-2^62,2^62)", value)
	}
}

func TestShufflePreservesCards(t *testing.T) {
	for _, cards := range [][]int{nil, {1}, {1, 1, 2, 3}} {
		shuffled := slices.Clone(cards)
		SliceShuffle(shuffled)
		slices.Sort(shuffled)
		if !slices.Equal(shuffled, cards) {
			t.Fatalf("shuffle changed the cards: got %v, want %v", shuffled, cards)
		}
	}
}

func TestSliceSubtractRespectsMultiplicity(t *testing.T) {
	values := []int{1, 2, 1, 3}
	result := SliceSubtract(values, []int{1})
	if !reflect.DeepEqual(result, []int{2, 1, 3}) {
		t.Fatalf("SliceSubtract() = %#v", result)
	}
	if !reflect.DeepEqual(values, []int{1, 2, 1, 3}) {
		t.Fatalf("SliceSubtract() modified input: %#v", values)
	}
}

func TestToJSON(t *testing.T) {
	if got := ToJSON(map[string]int{"value": 7}); got != `{"value":7}` {
		t.Fatalf("ToJSON() = %q", got)
	}
}
