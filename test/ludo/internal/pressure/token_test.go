package pressure

import "testing"

func TestTokenRoundTrip(t *testing.T) {
	want := MoneyRange{Min: 200, Max: 1000}
	token, err := Token(want)
	if err != nil {
		t.Fatal(err)
	}
	got, pressure, err := ParseToken(token)
	if err != nil || !pressure || got != want {
		t.Fatalf("ParseToken(%q) = (%+v,%v,%v)", token, got, pressure, err)
	}
}

func TestParseToken(t *testing.T) {
	for _, test := range []struct {
		name     string
		token    string
		pressure bool
		wantErr  bool
	}{
		{name: "normal token", token: "valid"},
		{name: "missing range", token: "pressure:", pressure: true, wantErr: true},
		{name: "invalid minimum", token: "pressure:min:100", pressure: true, wantErr: true},
		{name: "reversed", token: "pressure:200:100", pressure: true, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, pressure, err := ParseToken(test.token)
			if pressure != test.pressure || (err != nil) != test.wantErr {
				t.Fatalf("ParseToken(%q) = pressure:%v error:%v", test.token, pressure, err)
			}
		})
	}
}
