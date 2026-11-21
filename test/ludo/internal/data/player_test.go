package data

import (
	"strings"
	"testing"

	"yola/test/ludo/internal/biz/player"
	"yola/test/ludo/pkg/xredis"
)

func TestPlayerFromRedisDataRejectsInvalidNumericFields(t *testing.T) {
	for _, tc := range []struct {
		field string
		value string
	}{
		{field: xredis.PlayerUIDField, value: "not-an-id"},
		{field: xredis.PlayerMoneyField, value: "not-money"},
	} {
		t.Run(tc.field, func(t *testing.T) {
			data := playerRedisFields(&player.BaseData{UID: 42})
			data[tc.field] = tc.value

			_, err := playerFromRedisData(42, data)
			if err == nil || !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("playerFromRedisData() error = %v, want field name", err)
			}
			if strings.Contains(err.Error(), tc.value) {
				t.Fatalf("playerFromRedisData() exposed corrupted value: %v", err)
			}
		})
	}
}
