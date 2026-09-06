package xredis

import (
	"slices"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestOptions(t *testing.T) {
	addrs := []string{" redis.example.com:6380 ", "[::1]:6381"}
	options := validOptions()
	for _, option := range []Option{
		WithAddress(addrs...),
		WithPassword("secret"),
		WithDB(2),
		WithPoolSize(20),
		WithMinIdleConn(3),
		WithMaxIdleConn(8),
		WithConnMaxLifetime(time.Minute),
		WithConnMaxIdleTime(2 * time.Minute),
	} {
		option(options)
	}
	if err := validateOptions(options); err != nil {
		t.Fatal(err)
	}
	addrs[0] = "changed:6379"

	if want := []string{"redis.example.com:6380", "[::1]:6381"}; !slices.Equal(options.Addrs, want) {
		t.Fatalf("addresses = %v, want %v", options.Addrs, want)
	}
	if options.Password != "secret" || options.DB != 2 || options.PoolSize != 20 ||
		options.MinIdleConns != 3 || options.MaxIdleConns != 8 ||
		options.ConnMaxLifetime != time.Minute || options.ConnMaxIdleTime != 2*time.Minute {
		t.Fatalf("resolved options = %+v", options)
	}
}

func TestOptionsRejectInvalidValues(t *testing.T) {
	tests := []struct {
		name   string
		option Option
	}{
		{name: "empty addresses", option: WithAddress()},
		{name: "invalid second address", option: WithAddress("redis.example.com:6379", "invalid")},
		{name: "missing port", option: WithAddress("redis.example.com")},
		{name: "empty host", option: WithAddress(":6379")},
		{name: "zero port", option: WithAddress("redis.example.com:0")},
		{name: "large port", option: WithAddress("redis.example.com:65536")},
		{name: "negative database", option: WithDB(-1)},
		{name: "zero pool", option: WithPoolSize(0)},
		{name: "negative minimum idle", option: WithMinIdleConn(-1)},
		{name: "negative maximum idle", option: WithMaxIdleConn(-1)},
		{name: "zero lifetime", option: WithConnMaxLifetime(0)},
		{name: "zero idle time", option: WithConnMaxIdleTime(0)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := validOptions()
			test.option(options)
			if err := validateOptions(options); err == nil {
				t.Fatal("validateOptions() error = nil")
			}
		})
	}
}

func validOptions() *redis.UniversalOptions {
	return &redis.UniversalOptions{
		Addrs:           []string{"redis.example.com:6379"},
		PoolSize:        1,
		ConnMaxLifetime: time.Second,
		ConnMaxIdleTime: time.Second,
	}
}
