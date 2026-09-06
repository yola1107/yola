package websocket

import (
	"errors"
	"fmt"
	"testing"

	"yola/api/protocol/v1"
	"yola/network"

	"github.com/go-kratos/kratos/v3/encoding"
	"google.golang.org/protobuf/proto"
)

func TestUnmarshalFrameDiscardsUnknownFields(t *testing.T) {
	body, err := proto.Marshal(&v1.Proto{Op: v1.OpRequest})
	if err != nil {
		t.Fatal(err)
	}
	body = append(body, 0x7a, 0x01, 'x')
	got := new(v1.Proto)
	if err := unmarshalFrame(defaultCodec(), body, got); err != nil {
		t.Fatal(err)
	}
	if unknown := got.ProtoReflect().GetUnknown(); len(unknown) != 0 {
		t.Fatalf("unknown fields = %x", unknown)
	}
}

func TestUnmarshalFrameRejectsInvalidSize(t *testing.T) {
	if err := unmarshalFrame(defaultCodec(), nil, new(v1.Proto)); !errors.Is(err, errFrameLength) {
		t.Fatalf("unmarshalFrame(empty) error = %v, want %v", err, errFrameLength)
	}
	if err := unmarshalFrame(defaultCodec(), make([]byte, v1.MaxProtoSize+1), new(v1.Proto)); !errors.Is(err, network.ErrFrameTooLarge) {
		t.Fatalf("unmarshalFrame(oversized) error = %v, want %v", err, network.ErrFrameTooLarge)
	}
	if err := unmarshalFrame(defaultCodec(), []byte{0xff}, new(v1.Proto)); !errors.Is(err, errInvalidFrame) {
		t.Fatalf("unmarshalFrame(invalid) error = %v, want %v", err, errInvalidFrame)
	}
}

func TestDefaultCodecIgnoresGlobalProtoOverride(t *testing.T) {
	previous := encoding.GetCodec(defaultCodecName)
	if previous == nil {
		t.Fatal("Kratos protobuf codec is not registered")
	}
	override := new(countingCodec)
	encoding.RegisterCodec(override)
	t.Cleanup(func() { encoding.RegisterCodec(previous) })

	want := &v1.Proto{Op: v1.OpPush, Cmd: 7, Body: []byte("trusted")}
	body, err := marshalFrame(defaultCodec(), want)
	if err != nil {
		t.Fatal(err)
	}
	if override.marshals.Load() != 0 {
		t.Fatal("default codec used the global proto override")
	}
	got := new(v1.Proto)
	if err := proto.Unmarshal(body, got); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(got, want) {
		t.Fatalf("decoded message = %v, want %v", got, want)
	}
}

func TestDefaultCodecRejectsNonProtoValue(t *testing.T) {
	if _, err := defaultCodec().Marshal(struct{}{}); err == nil {
		t.Fatal("Marshal() accepted a non-protobuf value")
	}
	if err := defaultCodec().Unmarshal([]byte{1}, new(struct{})); err == nil {
		t.Fatal("Unmarshal() accepted a non-protobuf value")
	}
}

func BenchmarkCodec(b *testing.B) {
	codec := defaultCodec()
	for _, payloadSize := range []int{32, 4000} {
		p := &v1.Proto{Op: v1.OpRequest, Cmd: 1, Body: make([]byte, payloadSize)}
		body, err := marshalFrame(codec, p)
		if err != nil {
			b.Fatal(err)
		}
		name := fmt.Sprintf("payload=%d", payloadSize)

		b.Run(name+"/decode", func(b *testing.B) {
			benchmarkDecode(b, codec, body)
		})
		b.Run(name+"/encode", func(b *testing.B) {
			benchmarkEncode(b, codec, p)
		})
	}
}

func benchmarkDecode(b *testing.B, codec encoding.Codec, body []byte) {
	p := new(v1.Proto)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		if err := unmarshalFrame(codec, body, p); err != nil {
			b.Fatal(err)
		}
		proto.Reset(p)
	}
}

func benchmarkEncode(b *testing.B, codec encoding.Codec, p *v1.Proto) {
	b.SetBytes(int64(proto.Size(p)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := marshalFrame(codec, p); err != nil {
			b.Fatal(err)
		}
	}
}
