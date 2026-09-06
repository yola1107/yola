package event

import "testing"

func TestValidTopic(t *testing.T) {
	tests := []struct {
		topic string
		valid bool
	}{
		{topic: "yola.event.test", valid: true},
		{topic: "世界.event.v1", valid: true},
		{topic: ""},
		{topic: ".yola"},
		{topic: "yola."},
		{topic: "yola..event"},
		{topic: "yola.*"},
		{topic: "yola.>"},
		{topic: "yola event"},
		{topic: "yola\nevent"},
	}
	for _, test := range tests {
		t.Run(test.topic, func(t *testing.T) {
			if actual := ValidTopic(test.topic); actual != test.valid {
				t.Fatalf("ValidTopic(%q) = %t, want %t", test.topic, actual, test.valid)
			}
		})
	}
}
