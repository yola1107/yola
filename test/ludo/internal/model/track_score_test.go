package model

import "testing"

func TestCanMoveOneReturnsFailureReason(t *testing.T) {
	board := NewBoard([]int32{0}, 1, false)
	tests := []struct {
		name string
		id   int32
		dice int32
		code int32
	}{
		{name: "invalid piece", id: 1, dice: 6, code: ErrInvalidPieceIdx},
		{name: "idle piece requires six", id: 0, dice: 5, code: ErrIdleMustBeSix},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ok, code := board.canMoveOne(test.id, test.dice)
			if ok || code != test.code {
				t.Fatalf("canMoveOne(%d, %d) = (%t, %d), want (false, %d)", test.id, test.dice, ok, code, test.code)
			}
		})
	}
}

func TestFindBestMoveSequenceDoesNotMutateBoard(t *testing.T) {
	board := NewBoard([]int32{0, 1}, 1, false)
	pieceID, dice := FindBestMoveSequence(board, []int32{6, 1}, 0)
	if pieceID != 0 || dice != 6 {
		t.Fatalf("FindBestMoveSequence() = (%d, %d), want (0, 6)", pieceID, dice)
	}
	for _, piece := range board.Pieces() {
		if piece.Pos() != BasePos || piece.Status() != int32(PieceIdle) {
			t.Fatalf("piece %d changed to pos=%d status=%d", piece.ID(), piece.Pos(), piece.Status())
		}
	}
	if len(board.steps) != 0 {
		t.Fatalf("board recorded %d search steps", len(board.steps))
	}
}
