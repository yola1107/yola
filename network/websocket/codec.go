package websocket

import (
	"errors"
	"fmt"
	"io"

	"yola/api/protocol/v1"
	"yola/network"

	"github.com/go-kratos/kratos/v3/encoding"
	// 为使用全局 encoding registry 的 caller 注册 Kratos protobuf codec。
	_ "github.com/go-kratos/kratos/v3/encoding/proto"
	"google.golang.org/protobuf/proto"
)

var (
	errNilPayload   = errors.New("websocket: nil payload")
	errFrameLength  = errors.New("websocket: invalid frame length")
	errInvalidFrame = errors.New("websocket: invalid frame")
)

const _defaultCodecName = "proto"

// defaultFrameCodec 由包内持有，防止全局 codec 注册
// 改变其协议格式或输入持有行为。
type defaultFrameCodec struct{}

func defaultCodec() encoding.Codec {
	return defaultFrameCodec{}
}

func marshalFrame(codec encoding.Codec, p *v1.Proto) ([]byte, error) {
	if p == nil {
		return nil, errNilPayload
	}
	body, err := codec.Marshal(p)
	if err != nil {
		return nil, err
	}
	return body, validateFrameBody(body)
}

func unmarshalFrame(codec encoding.Codec, body []byte, p *v1.Proto) error {
	if err := validateFrameBody(body); err != nil {
		return err
	}
	if err := codec.Unmarshal(body, p); err != nil {
		return fmt.Errorf("%w: %v", errInvalidFrame, err)
	}
	p.ProtoReflect().SetUnknown(nil)
	return nil
}

func readBoundedFrame(reader io.Reader, buffer []byte) ([]byte, error) {
	read := 0
	for {
		if read == len(buffer) {
			var extra [1]byte
			n, err := reader.Read(extra[:])
			switch {
			case n > 0:
				return nil, network.ErrFrameTooLarge
			case errors.Is(err, io.EOF):
				return buffer[:read], nil
			case err != nil:
				return nil, err
			default:
				continue
			}
		}
		n, err := reader.Read(buffer[read:])
		read += n
		switch {
		case errors.Is(err, io.EOF):
			return buffer[:read], nil
		case err != nil:
			return nil, err
		}
	}
}

func canOptimizeFrames(codec encoding.Codec) bool {
	_, ok := codec.(defaultFrameCodec)
	return ok
}

func validateFrameBody(body []byte) error {
	if len(body) == 0 {
		return errFrameLength
	}
	if len(body) > v1.MaxProtoSize {
		return network.ErrFrameTooLarge
	}
	return nil
}

func (defaultFrameCodec) Marshal(value any) ([]byte, error) {
	message, ok := value.(proto.Message)
	if !ok {
		return nil, fmt.Errorf("websocket: %T is not a protobuf message", value)
	}
	return proto.Marshal(message)
}

func (defaultFrameCodec) Unmarshal(data []byte, value any) error {
	message, ok := value.(proto.Message)
	if !ok {
		return fmt.Errorf("websocket: %T is not a protobuf message", value)
	}
	return proto.Unmarshal(data, message)
}

func (defaultFrameCodec) Name() string { return _defaultCodecName }
