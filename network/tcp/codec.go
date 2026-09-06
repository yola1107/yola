package tcp

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"

	"yola/api/protocol/v1"
	"yola/network"

	"github.com/go-kratos/kratos/v3/encoding"
	encodingproto "github.com/go-kratos/kratos/v3/encoding/proto"
	"google.golang.org/protobuf/proto"
)

const (
	framePrefixSize     = 4
	defaultIOBufferSize = v1.MaxProtoSize + framePrefixSize
)

var (
	errFrameLength  = errors.New("tcp: invalid frame length")
	errInvalidFrame = errors.New("tcp: invalid frame")
)

var defaultProtoCodec = encoding.GetCodec(encodingproto.Name)

func defaultCodec() encoding.Codec {
	return protoFrameCodec{Codec: defaultProtoCodec}
}

func readFrame(rr *bufio.Reader, codec encoding.Codec, p *v1.Proto) error {
	prefix, err := rr.Peek(framePrefixSize)
	if err != nil {
		return err
	}
	length := binary.LittleEndian.Uint32(prefix)
	if sizeErr := validateFrameSize(int(length)); sizeErr != nil {
		return sizeErr
	}
	if _, err = rr.Discard(framePrefixSize); err != nil {
		return err
	}
	body, err := rr.Peek(int(length))
	if err != nil {
		return err
	}
	if unmarshalErr := codec.Unmarshal(body, p); unmarshalErr != nil {
		return fmt.Errorf("%w: %v", errInvalidFrame, unmarshalErr)
	}
	p.ProtoReflect().SetUnknown(nil)
	_, err = rr.Discard(int(length))
	return err
}

func writeFrame(wr *bufio.Writer, codec encoding.Codec, p *v1.Proto) error {
	if appender, ok := codec.(frameAppender); ok {
		return writeFrameAppend(wr, appender, p)
	}
	body, err := codec.Marshal(p)
	if err != nil {
		return err
	}
	length := len(body)
	if err = validateFrameSize(length); err != nil {
		return err
	}
	var prefix [framePrefixSize]byte
	binary.LittleEndian.PutUint32(prefix[:], uint32(length))
	if _, err = wr.Write(prefix[:]); err != nil {
		return err
	}
	_, err = wr.Write(body)
	return err
}

func writeFrameAppend(wr *bufio.Writer, codec frameAppender, p *v1.Proto) error {
	length := codec.size(p)
	if err := validateFrameSize(length); err != nil {
		return err
	}
	if wr.Available() < framePrefixSize+length {
		if err := wr.Flush(); err != nil {
			return err
		}
	}
	frame := binary.LittleEndian.AppendUint32(wr.AvailableBuffer(), uint32(length))
	frame, err := codec.marshalAppend(frame, p)
	if err != nil {
		return err
	}
	_, err = wr.Write(frame)
	return err
}

func validateFrame(codec encoding.Codec, p *v1.Proto) error {
	if appender, ok := codec.(frameAppender); ok {
		return validateFrameSize(appender.size(p))
	}
	body, err := codec.Marshal(p)
	if err != nil {
		return err
	}
	return validateFrameSize(len(body))
}

func validateFrameSize(size int) error {
	if size == 0 {
		return errFrameLength
	}
	if size > v1.MaxProtoSize {
		return network.ErrFrameTooLarge
	}
	return nil
}

type frameAppender interface {
	size(*v1.Proto) int
	marshalAppend([]byte, *v1.Proto) ([]byte, error)
}

type protoFrameCodec struct {
	encoding.Codec
}

func (protoFrameCodec) size(p *v1.Proto) int {
	return proto.Size(p)
}

func (protoFrameCodec) marshalAppend(dst []byte, p *v1.Proto) ([]byte, error) {
	return proto.MarshalOptions{}.MarshalAppend(dst, p)
}
