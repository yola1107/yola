package auth

import (
	"errors"
	"io"
	"testing"

	"yola/api/protocol/v1"

	"github.com/stretchr/testify/require"
)

func TestReadReply(t *testing.T) {
	rejected := errors.New("authentication rejected")
	first := &v1.Proto{Op: v1.OpPush, Cmd: 1, Body: []byte("first")}
	second := &v1.Proto{Op: v1.OpPush, Cmd: 2, Body: []byte("second")}
	ok := &v1.Proto{Op: v1.OpAuthReply}
	for _, test := range []struct {
		name     string
		messages []*v1.Proto
		limit    int
		want     []*v1.Proto
		wantErr  error
		wantText string
		reads    int
	}{
		{name: "no pending push", messages: []*v1.Proto{ok, first}, reads: 1},
		{
			name: "ordered pushes at capacity", messages: []*v1.Proto{first, second, ok}, limit: 2,
			want: []*v1.Proto{first, second}, reads: 3,
		},
		{
			name: "push limit", messages: []*v1.Proto{first, second, ok}, limit: 1,
			wantText: "tcp: too many pushes before authentication reply", reads: 2,
		},
		{
			name: "wrong operation", messages: []*v1.Proto{{Op: v1.OpResponse}},
			wantText: "tcp: invalid authentication response", reads: 1,
		},
		{
			name: "rejected after push", messages: []*v1.Proto{first, {Op: v1.OpAuthReply, Code: 16}}, limit: 1,
			wantErr: rejected, wantText: "authentication rejected: code=16", reads: 2,
		},
		{
			name: "read failure after push", messages: []*v1.Proto{first}, limit: 1,
			wantErr: io.EOF, reads: 2,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			reads := 0
			pending, err := ReadReply("tcp", rejected, test.limit, func() (*v1.Proto, error) {
				reads++
				if reads > len(test.messages) {
					return nil, io.EOF
				}
				return test.messages[reads-1], nil
			})
			if test.wantErr != nil {
				require.ErrorIs(t, err, test.wantErr)
			} else if test.wantText == "" {
				require.NoError(t, err)
			}
			if test.wantText != "" {
				require.EqualError(t, err, test.wantText)
			}
			require.Equal(t, test.want, pending)
			require.Equal(t, test.reads, reads)
			if test.wantErr == io.EOF {
				require.Same(t, io.EOF, err)
			}
		})
	}
}
