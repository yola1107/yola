package data

import (
	"strings"
	"testing"

	"yola/test/whot/internal/biz/player"
	"yola/test/whot/pkg/xredis"
)

func TestDecodeBaseDataRejectsCorruptUID(t *testing.T) {
	values := validRedisValues(42)
	values[fieldIndex(t, xredis.PlayerUIDField)] = "secret-invalid-uid"
	_, err := decodeBaseData(42, values)
	assertFieldError(t, err, xredis.PlayerUIDField, "secret-invalid-uid")
}

func TestDecodeBaseDataRejectsCorruptMoney(t *testing.T) {
	values := validRedisValues(42)
	values[fieldIndex(t, xredis.PlayerMoneyField)] = "NaN"
	_, err := decodeBaseData(42, values)
	assertFieldError(t, err, xredis.PlayerMoneyField, "NaN")
}

func TestDecodeBaseDataRejectsMissingOrUnexpectedValues(t *testing.T) {
	for _, test := range []struct {
		name     string
		value    any
		truncate bool
	}{
		{name: "nil"},
		{name: "number", value: 7},
		{name: "missing", truncate: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			values := validRedisValues(42)
			index := fieldIndex(t, xredis.PlayerVIPField)
			values[index] = test.value
			if test.truncate {
				values = values[:index]
			}
			_, err := decodeBaseData(42, values)
			assertFieldError(t, err, xredis.PlayerVIPField, "sensitive-value")
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

func fieldIndex(t *testing.T, wanted string) int {
	t.Helper()
	for index, field := range playerFields {
		if field == wanted {
			return index
		}
	}
	t.Fatalf("field %q not found", wanted)
	return -1
}

func assertFieldError(t *testing.T, err error, field, sensitiveValue string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %q decode error", field)
	}
	if !strings.Contains(err.Error(), field) {
		t.Fatalf("error %q does not identify field %q", err, field)
	}
	if strings.Contains(err.Error(), sensitiveValue) {
		t.Fatalf("error leaks rejected value: %q", err)
	}
}
