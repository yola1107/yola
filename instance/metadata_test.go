package instance

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsSticky(t *testing.T) {
	tests := []struct {
		name     string
		metadata map[string]string
		want     bool
		wantErr  string
	}{
		{name: "nil"},
		{name: "missing", metadata: map[string]string{"other": "value"}},
		{name: "enabled", metadata: map[string]string{StickyMetadataKey: "true"}, want: true},
		{name: "disabled", metadata: map[string]string{StickyMetadataKey: "false"}},
		{name: "invalid", metadata: map[string]string{StickyMetadataKey: "yes"}, wantErr: "invalid sticky metadata"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := IsSticky(test.metadata)
			if test.wantErr != "" {
				require.ErrorContains(t, err, test.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}
}

func TestStickyMetadataReturnsIndependentMaps(t *testing.T) {
	first := StickyMetadata()
	first[StickyMetadataKey] = "false"

	require.Equal(t, "true", StickyMetadata()[StickyMetadataKey])
}
