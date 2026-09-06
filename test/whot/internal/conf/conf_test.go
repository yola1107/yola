package conf

import (
	"strings"
	"testing"

	"github.com/go-kratos/kratos/v3/config"
	"github.com/go-kratos/kratos/v3/config/file"
)

func TestConfigFileValidates(t *testing.T) {
	c := config.New(config.WithSource(file.NewSource("../../configs")))
	t.Cleanup(func() { _ = c.Close() })
	if err := c.Load(); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	var bootstrap Bootstrap
	if err := c.Scan(&bootstrap); err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if err := bootstrap.ValidateConfig(); err != nil {
		t.Fatalf("ValidateConfig() error = %v", err)
	}
}

func TestBootstrapValidateConfigRejectsMissingLocatorConfig(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Bootstrap)
		want   string
	}{
		{name: "Redis", change: func(bootstrap *Bootstrap) { bootstrap.Data.Redis = nil }, want: "data.redis"},
		{name: "Registry", change: func(bootstrap *Bootstrap) { bootstrap.Data.Registry = nil }, want: "data.registry"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bootstrap := validBootstrap()
			test.change(bootstrap)
			if err := bootstrap.ValidateConfig(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateConfig() error = %v, want %q", err, test.want)
			}
		})
	}
}

func validBootstrap() *Bootstrap {
	return &Bootstrap{
		Server: &Server{Grpc: &Server_GRPC{}},
		Data: &Data{
			Redis:    &Data_Redis{},
			Registry: &Data_Registry{},
		},
		Room: &Room{
			Table: &Room_Table{TableNum: 1, ChairNum: 4},
			Game:  &Room_Game{},
			Robot: &Room_Robot{},
		},
	}
}
