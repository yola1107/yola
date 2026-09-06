package model

import "testing"

func TestCanMovePiece(t *testing.T) {
	tests := []struct {
		name  string
		pos   int32
		color int32
		state PieceState
		steps int32
		want  bool
	}{
		{name: "leave base", pos: BasePos, color: 0, state: PieceIdle, steps: 6, want: true},
		{name: "stay at base", pos: BasePos, color: 0, state: PieceIdle, steps: 5, want: false},
		{name: "public track", pos: 12, color: 1, state: PieceOnBoard, steps: 4, want: true},
		{name: "home path exact", pos: 104, color: 0, state: PieceInHomePath, steps: 2, want: true},
		{name: "home path overflow", pos: 104, color: 0, state: PieceInHomePath, steps: 3, want: false},
		{name: "arrived", pos: 106, color: 0, state: PieceArrived, steps: 1, want: false},
		{name: "inconsistent state", pos: 0, color: 0, state: PieceArrived, steps: 1, want: false},
		{name: "invalid state", pos: 0, color: 0, state: PieceState(4), steps: 1, want: false},
		{name: "invalid color", pos: 0, color: ColorCount, state: PieceOnBoard, steps: 1, want: false},
		{name: "invalid steps", pos: 0, color: 0, state: PieceOnBoard, steps: 7, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := CanMovePiece(test.pos, test.color, int32(test.state), test.steps); got != test.want {
				t.Fatalf("CanMovePiece(%d, %d, %d, %d) = %t, want %t", test.pos, test.color, test.state, test.steps, got, test.want)
			}
		})
	}
}
