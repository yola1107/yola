package data

import (
	"slices"
	"testing"

	"yola/test/whot/internal/biz/player"
	"yola/test/whot/pkg/xredis"
)

func TestDecodeBaseDataRejectsCorruptFields(t *testing.T) {
	for _, test := range []struct {
		name        string
		change      map[string]any
		truncateAt  string
		wantMessage string
	}{
		{
			name:        "malformed UID",
			change:      map[string]any{xredis.PlayerUIDField: "secret-invalid-uid"},
			wantMessage: `decode Redis field "uid": invalid int64`,
		},
		{
			name:        "first malformed numeric field",
			change:      map[string]any{xredis.PlayerMoneyField: "NaN", xredis.PlayerAllTotalBoardField: "invalid-board"},
			wantMessage: `decode Redis field "money": invalid float64`,
		},
		{
			name:        "nil field",
			change:      map[string]any{xredis.PlayerVIPField: nil},
			wantMessage: `decode Redis field "vip": value is missing`,
		},
		{
			name:        "wrong field type",
			change:      map[string]any{xredis.PlayerVIPField: 7},
			wantMessage: `decode Redis field "vip": unexpected value type`,
		},
		{
			name:        "truncated result",
			truncateAt:  xredis.PlayerVIPField,
			wantMessage: `decode Redis field "vip": value is missing`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			values := validRedisValues(42)
			for field, value := range test.change {
				values[fieldIndex(t, field)] = value
			}
			if test.truncateAt != "" {
				values = values[:fieldIndex(t, test.truncateAt)]
			}
			_, err := decodeBaseData(42, values)
			if err == nil || err.Error() != test.wantMessage {
				t.Fatalf("decode error = %v, want %q", err, test.wantMessage)
			}
		})
	}
}

func TestDecodeBaseDataAcceptsByteValues(t *testing.T) {
	values := validRedisValues(42)
	for index, value := range values {
		text, ok := value.(string)
		if !ok {
			t.Fatalf("fixture field %d is not a string", index)
		}
		values[index] = []byte(text)
	}
	base, err := decodeBaseData(42, values)
	if err != nil {
		t.Fatal(err)
	}
	if base.UID != 42 || base.Money != 100 {
		t.Fatalf("decoded player = %+v, want UID 42 and money 100", base)
	}
}

func TestPlayerRedisFieldsPreservesNumericEncoding(t *testing.T) {
	encoded := playerRedisFields(&player.BaseData{
		UID:           42,
		VIP:           7,
		Money:         12.345,
		AllTotalBoard: 99,
	})
	if encoded[xredis.PlayerUIDField] != "42" || encoded[xredis.PlayerVIPField] != "7" {
		t.Fatalf("integer encoding = uid:%q vip:%q", encoded[xredis.PlayerUIDField], encoded[xredis.PlayerVIPField])
	}
	if encoded[xredis.PlayerMoneyField] != "12.35" {
		t.Fatalf("money encoding = %q, want 12.35", encoded[xredis.PlayerMoneyField])
	}
	if encoded[xredis.PlayerAllTotalBoardField] != "99" {
		t.Fatalf("board encoding = %q, want 99", encoded[xredis.PlayerAllTotalBoardField])
	}
}

func validRedisValues(uid int64) []any {
	encoded := playerRedisFields(&player.BaseData{UID: uid, Money: 100})
	values := make([]any, len(playerFields))
	for index, field := range playerFields {
		values[index] = encoded[field]
	}
	return values
}

func fieldIndex(t *testing.T, field string) int {
	t.Helper()
	index := slices.Index(playerFields, field)
	if index < 0 {
		t.Fatalf("field %q not found", field)
	}
	return index
}
