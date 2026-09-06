package websocket

import (
	"errors"
	"fmt"

	"yola/api/protocol/v1"
	"yola/network"

	"github.com/go-kratos/kratos/v3/encoding"
	// Register the Kratos protobuf codec for callers that use the global encoding registry.
	_ "github.com/go-kratos/kratos/v3/encoding/proto"
	"google.golang.org/protobuf/proto"
)

var (
	errFrameLength  = errors.New("websocket: invalid frame length")
	errInvalidFrame = errors.New("websocket: invalid frame")
)

const defaultCodecName = "proto"

// defaultFrameCodec is package-owned so global codec registration cannot
// change its wire format or input-retention behavior.
type defaultFrameCodec struct{}

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

func (defaultFrameCodec) Name() string { return defaultCodecName }

func defaultCodec() encoding.Codec {
	return defaultFrameCodec{}
}

func canOptimizeFrames(codec encoding.Codec) bool {
	_, ok := codec.(defaultFrameCodec)
	return ok
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

func validateFrameBody(body []byte) error {
	if len(body) == 0 {
		return errFrameLength
	}
	if len(body) > v1.MaxProtoSize {
		return network.ErrFrameTooLarge
	}
	return nil
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
