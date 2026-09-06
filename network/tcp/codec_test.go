package tcp

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"testing"

	"yola/api/protocol/v1"
	"yola/network"

	"google.golang.org/protobuf/proto"
)

func TestCodecRoundTrip(t *testing.T) {
	codec := defaultCodec()
	wants := []*v1.Proto{
		{Op: v1.OpRequest, Seq: 1, Code: 3, Cmd: 1001, Body: []byte("small")},
		{Op: v1.OpPush, Seq: 2, Body: make([]byte, 4000)},
	}
	var wire bytes.Buffer
	wr := bufio.NewWriterSize(&wire, 64)
	for _, want := range wants {
		if err := writeFrame(wr, codec, want); err != nil {
			t.Fatal(err)
		}
	}
	if err := wr.Flush(); err != nil {
		t.Fatal(err)
	}
	rr := bufio.NewReaderSize(&wire, defaultIOBufferSize)
	for _, want := range wants {
		got := new(v1.Proto)
		if err := readFrame(rr, codec, got); err != nil {
			t.Fatal(err)
		}
		if !proto.Equal(want, got) {
			t.Fatalf("want %v, got %v", want, got)
		}
	}
}

func TestWriteFrameRejectsInvalidSize(t *testing.T) {
	tests := []struct {
		name    string
		message *v1.Proto
		err     error
	}{
		{name: "empty", message: new(v1.Proto), err: errFrameLength},
		{name: "oversized", message: &v1.Proto{
			Op:   v1.OpRequest,
			Body: make([]byte, v1.MaxProtoSize),
		}, err: network.ErrFrameTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := writeFrame(bufio.NewWriter(io.Discard), defaultCodec(), tt.message)
			if !errors.Is(err, tt.err) {
				t.Fatalf("writeFrame() error = %v, want %v", err, tt.err)
			}
		})
	}
}

func TestReadFrameRejectsInvalidFrame(t *testing.T) {
	tests := []struct {
		name string
		wire []byte
		err  error
	}{
		{name: "zero length", wire: frameWithLength(0, nil), err: errFrameLength},
		{name: "oversized", wire: frameWithLength(v1.MaxProtoSize+1, nil), err: network.ErrFrameTooLarge},
		{name: "truncated", wire: frameWithLength(3, []byte{0x08}), err: io.EOF},
		{name: "invalid protobuf", wire: frameWithLength(1, []byte{0xff}), err: errInvalidFrame},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := readFrame(bufio.NewReader(bytes.NewReader(tt.wire)), defaultCodec(), new(v1.Proto))
			if !errors.Is(err, tt.err) {
				t.Fatalf("want %v, got %v", tt.err, err)
			}
		})
	}
}

type fixedSizeCodec struct {
	size int
}

func (c fixedSizeCodec) Marshal(any) ([]byte, error) { return make([]byte, c.size), nil }
func (fixedSizeCodec) Unmarshal([]byte, any) error   { return nil }
func (fixedSizeCodec) Name() string                  { return "fixed-size" }

func TestReadFrameDiscardsUnknownFields(t *testing.T) {
	body, err := proto.Marshal(&v1.Proto{Op: v1.OpRequest})
	if err != nil {
		t.Fatal(err)
	}
	body = append(body, 0x7a, 0x01, 'x')
	got := new(v1.Proto)
	if err := readFrame(bufio.NewReader(bytes.NewReader(frameWithLength(uint32(len(body)), body))), defaultCodec(), got); err != nil {
		t.Fatal(err)
	}
	if unknown := got.ProtoReflect().GetUnknown(); len(unknown) != 0 {
		t.Fatalf("unknown fields = %x", unknown)
	}
}

func frameWithLength(length uint32, body []byte) []byte {
	wire := make([]byte, framePrefixSize+len(body))
	binary.LittleEndian.PutUint32(wire, length)
	copy(wire[framePrefixSize:], body)
	return wire
}

func BenchmarkCodec(b *testing.B) {
	for _, payloadSize := range []int{32, 4000} {
		p := &v1.Proto{Op: v1.OpRequest, Cmd: 1, Body: make([]byte, payloadSize)}
		body, err := proto.Marshal(p)
		if err != nil {
			b.Fatal(err)
		}
		frame := frameWithLength(uint32(len(body)), body)
		name := fmt.Sprintf("payload=%d", payloadSize)

		b.Run(name+"/read", func(b *testing.B) { benchmarkRead(b, frame) })
		b.Run(name+"/write", func(b *testing.B) { benchmarkWrite(b, p) })
	}
}

func benchmarkRead(b *testing.B, frame []byte) {
	var source bytes.Reader
	rr := bufio.NewReaderSize(&source, defaultIOBufferSize)
	p := new(v1.Proto)
	codec := defaultCodec()
	b.SetBytes(int64(len(frame)))
	b.ReportAllocs()
	for b.Loop() {
		source.Reset(frame)
		rr.Reset(&source)
		if err := readFrame(rr, codec, p); err != nil {
			b.Fatal(err)
		}
		proto.Reset(p)
	}
}

func benchmarkWrite(b *testing.B, p *v1.Proto) {
	wr := bufio.NewWriterSize(io.Discard, defaultIOBufferSize)
	codec := defaultCodec()
	b.SetBytes(int64(proto.Size(p) + framePrefixSize))
	b.ReportAllocs()
	for b.Loop() {
		if err := writeFrame(wr, codec, p); err != nil {
			b.Fatal(err)
		}
		if err := wr.Flush(); err != nil {
			b.Fatal(err)
		}
	}
}
