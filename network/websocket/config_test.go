package websocket

import (
	"testing"
	"time"
)

func TestChannelConfigValidate(t *testing.T) {
	tests := []struct {
		name   string
		config ChannelConfig
		want   string
	}{
		{
			name:   "write timeout",
			config: ChannelConfig{ReadDeadline: time.Second, SendQueueSize: 1},
			want:   "websocket: channel write timeout must be positive",
		},
		{
			name:   "read deadline",
			config: ChannelConfig{WriteTimeout: time.Second, SendQueueSize: 1},
			want:   "websocket: channel read deadline must be positive",
		},
		{
			name:   "send queue size",
			config: ChannelConfig{WriteTimeout: time.Second, ReadDeadline: time.Second},
			want:   "websocket: channel send queue size must be positive",
		},
		{
			name:   "valid",
			config: ChannelConfig{WriteTimeout: time.Second, ReadDeadline: time.Second, SendQueueSize: 1},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.config.validate()
			if test.want == "" {
				if err != nil {
					t.Fatalf("validate() error = %v, want nil", err)
				}
				return
			}
			if err == nil || err.Error() != test.want {
				t.Fatalf("validate() error = %v, want %q", err, test.want)
			}
		})
	}
}
