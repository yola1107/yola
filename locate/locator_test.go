package locate

import (
	"strings"
	"testing"
)

func TestValidServiceName(t *testing.T) {
	for _, test := range []struct {
		name string
		want bool
	}{
		{name: "game.player-1", want: true},
		{name: strings.Repeat("a", 128), want: true},
		{name: ""},
		{name: "bad name"},
		{name: strings.Repeat("a", 129)},
	} {
		if got := ValidServiceName(test.name); got != test.want {
			t.Errorf("ValidServiceName(%q) = %t, want %t", test.name, got, test.want)
		}
	}
}

func TestValidUID(t *testing.T) {
	for _, test := range []struct {
		uid  string
		want bool
	}{
		{uid: "player:一", want: true},
		{uid: "player {a}\n", want: true},
		{uid: strings.Repeat("a", 128), want: true},
		{},
		{uid: string([]byte{0xff})},
		{uid: strings.Repeat("a", 129)},
	} {
		if got := ValidUID(test.uid); got != test.want {
			t.Errorf("ValidUID(%q) = %t, want %t", test.uid, got, test.want)
		}
	}
}

func TestValidNodeLocation(t *testing.T) {
	if !ValidNodeLocation("whot", "player-a", "node-a") {
		t.Fatal("complete Node location should be valid")
	}
	if ValidNodeLocation("whot", "player-a", "") {
		t.Fatal("Node location without NodeID should be invalid")
	}
}
