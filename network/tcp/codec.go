package tcp

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"

	v1 "yola/api/protocol/v1"
	"yola/network"

	"github.com/go-kratos/kratos/v3/encoding"
	encodingproto "github.com/go-kratos/kratos/v3/encoding/proto"
	"google.golang.org/protobuf/proto"
)

const (
	_framePrefixSize     = 4
	_defaultIOBufferSize = v1.MaxProtoSize + _framePrefixSize
)

var (
	errFrameLength  = errors.New("tcp: invalid frame length")
	errInvalidFrame = errors.New("tcp: invalid frame")
)

var _defaultProtoCodec = encoding.GetCodec(encodingproto.Name)

func defaultCodec() encoding.Codec {
	return protoFrameCodec{Codec: _defaultProtoCodec}
}

func readFrame(rr *bufio.Reader, codec encoding.Codec, p *v1.Proto) error {
	prefix, err := rr.Peek(_framePrefixSize)
	if err != nil {
		return err
	}
	length := int(binary.LittleEndian.Uint32(prefix))
	if _, sizeErr := frameLength(length); sizeErr != nil {
		return sizeErr
	}
	if _, err = rr.Discard(_framePrefixSize); err != nil {
		return err
	}
	body, err := rr.Peek(length)
	if err != nil {
		return err
	}
	if unmarshalErr := codec.Unmarshal(body, p); unmarshalErr != nil {
		return fmt.Errorf("%w: %w", errInvalidFrame, unmarshalErr)
	}
	p.ProtoReflect().SetUnknown(nil)
	_, err = rr.Discard(length)
	return err
}

func writeFrame(wr *bufio.Writer, codec encoding.Codec, p *v1.Proto) error {
	if _, ok := codec.(protoFrameCodec); ok {
		return writeFrameAppend(wr, p)
	}
	body, err := codec.Marshal(p)
	if err != nil {
		return err
	}
	length, err := frameLength(len(body))
	if err != nil {
		return err
	}
	var prefix [_framePrefixSize]byte
	binary.LittleEndian.PutUint32(prefix[:], length)
	if _, err = wr.Write(prefix[:]); err != nil {
		return err
	}
	_, err = wr.Write(body)
	return err
}

func validateFrame(codec encoding.Codec, p *v1.Proto) error {
	if _, ok := codec.(protoFrameCodec); ok {
		_, err := frameLength(proto.Size(p))
		return err
	}
	body, err := codec.Marshal(p)
	if err != nil {
		return err
	}
	_, err = frameLength(len(body))
	return err
}

func writeFrameAppend(wr *bufio.Writer, p *v1.Proto) error {
	size := proto.Size(p)
	length, err := frameLength(size)
	if err != nil {
		return err
	}
	if wr.Available() < _framePrefixSize+size {
		if flushErr := wr.Flush(); flushErr != nil {
			return flushErr
		}
	}
	frame := binary.LittleEndian.AppendUint32(wr.AvailableBuffer(), length)
	frame, err = proto.MarshalOptions{}.MarshalAppend(frame, p)
	if err != nil {
		return err
	}
	_, err = wr.Write(frame)
	return err
}

func frameLength(size int) (uint32, error) {
	if size <= 0 {
		return 0, errFrameLength
	}
	if size > v1.MaxProtoSize {
		return 0, network.ErrFrameTooLarge
	}
	return uint32(size), nil
}

type protoFrameCodec struct {
	encoding.Codec
}
