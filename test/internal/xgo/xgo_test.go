package xgo

import (
	"reflect"
	"testing"
)

func TestRandomBounds(t *testing.T) {
	for range 1_000 {
		if value := RandInt(-3, 5); value < -3 || value >= 5 {
			t.Fatalf("RandInt() = %d outside [-3,5)", value)
		}
		if value := RandIntInclusive(-3, 5); value < -3 || value > 5 {
			t.Fatalf("RandIntInclusive() = %d outside [-3,5]", value)
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
