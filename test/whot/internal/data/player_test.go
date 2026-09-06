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
