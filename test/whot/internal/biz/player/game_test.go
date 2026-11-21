package player

import "testing"

func TestSetLostUsesLostStatus(t *testing.T) {
	p := New(&Raw{BaseData: &BaseData{UID: 1}})
	p.SetLost()
	if !p.IsLost() {
		t.Fatalf("status = %v, want %v", p.GetStatus(), StGameLost)
	}
	if p.IsFold() {
		t.Fatal("lost player must not be marked folded")
	}
}
