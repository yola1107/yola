package network

import (
	"bytes"
	"errors"
	"sync"
	"testing"

	"yola/api/protocol/v1"

	"google.golang.org/protobuf/proto"
)

func TestPreparedProtoSharesOneEncoding(t *testing.T) {
	message := &v1.Proto{Op: v1.OpPush, Body: []byte("first")}
	prepared := new(PreparedProto)
	prepared.Reset(message)

	const readers = 32
	results := make([][]byte, readers)
	errs := make([]error, readers)
	var wait sync.WaitGroup
	wait.Add(readers)
	for index := range readers {
		go func() {
			defer wait.Done()
			results[index], errs[index] = prepared.Marshal()
		}()
	}
	wait.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("Marshal() error = %v", err)
		}
	}
	shared := results[0]
	if len(shared) == 0 {
		t.Fatal("Marshal() returned an empty encoding")
	}
	for _, body := range results[1:] {
		if len(body) == 0 || &body[0] != &shared[0] {
			t.Fatal("Marshal() did not return the shared encoding")
		}
	}

	prepared.Reset(&v1.Proto{Op: v1.OpPush, Body: []byte("second")})
	body, err := prepared.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(body, shared) {
		t.Fatal("Reset() retained the previous encoding")
	}
	previous := new(v1.Proto)
	if err := proto.Unmarshal(shared, previous); err != nil {
		t.Fatal(err)
	}
	if string(previous.Body) != "first" {
		t.Fatalf("previous encoding changed after Reset(): %q", previous.Body)
	}
}

func TestPreparedProtoRejectsInvalidInput(t *testing.T) {
	nilMessage := new(PreparedProto)
	nilMessage.Reset(nil)
	for _, test := range []struct {
		name     string
		prepared *PreparedProto
	}{
		{name: "nil prepared"},
		{name: "missing state", prepared: new(PreparedProto)},
		{name: "nil message", prepared: nilMessage},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := test.prepared.Marshal(); !errors.Is(err, errInvalidPreparedProto) {
				t.Fatalf("Marshal() error = %v, want %v", err, errInvalidPreparedProto)
			}
		})
	}
}
