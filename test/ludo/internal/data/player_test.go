package data

import (
	"strings"
	"testing"

	"yola/test/ludo/internal/biz/player"
	"yola/test/ludo/pkg/xredis"
)

func TestPlayerFromRedisDataRejectsInvalidUID(t *testing.T) {
	const corrupted = "not-an-id"
	data := playerRedisFields(&player.BaseData{UID: 42})
	data[xredis.PlayerUIDField] = corrupted

	_, err := playerFromRedisData(42, data)
	if err == nil || !strings.Contains(err.Error(), xredis.PlayerUIDField) {
		t.Fatalf("playerFromRedisData() error = %v, want field name", err)
	}
	if strings.Contains(err.Error(), corrupted) {
		t.Fatalf("playerFromRedisData() exposed corrupted value: %v", err)
	}
}

func TestPlayerFromRedisDataRejectsInvalidMoney(t *testing.T) {
	const corrupted = "not-money"
	data := playerRedisFields(&player.BaseData{UID: 42})
	data[xredis.PlayerMoneyField] = corrupted

	_, err := playerFromRedisData(42, data)
	if err == nil || !strings.Contains(err.Error(), xredis.PlayerMoneyField) {
		t.Fatalf("playerFromRedisData() error = %v, want field name", err)
	}
	if strings.Contains(err.Error(), corrupted) {
		t.Fatalf("playerFromRedisData() exposed corrupted value: %v", err)
	}
}
