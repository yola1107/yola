package network

import (
	"errors"
	"sync"

	"yola/api/protocol/v1"

	"google.golang.org/protobuf/proto"
)

var errInvalidPreparedProto = errors.New("network: invalid prepared proto")

// PreparedProto 是一批有界发送期间对不可变 Proto 的同步视图。
// PreparedConnection.SendPrepared 返回后不得继续持有该视图；
// Reset 不得与上一条消息的发送调用重叠。
// 使用后不得复制。
type PreparedProto struct {
	message *v1.Proto
	state   *preparedState
	once    sync.Once
}

type preparedState struct {
	body []byte
	err  error
}

// Reset 在上一批 SendPrepared 全部返回后复用视图，不恢复旧 message 的写入权。
// fallback 连接仍可能持有旧 Proto；新旧消息及其字段都须遵守 SendProto 的不可变契约。
func (p *PreparedProto) Reset(message *v1.Proto) {
	p.message = message
	p.state = nil
	p.once = sync.Once{}
}

// Message 返回供 fallback 发送的只读 Proto，连接可按 SendProto 契约继续持有它。
func (p *PreparedProto) Message() *v1.Proto {
	if p == nil {
		return nil
	}
	return p.message
}

// Marshal 返回共用的 protobuf 编码结果；caller 不得修改。
func (p *PreparedProto) Marshal() ([]byte, error) {
	if p == nil || p.message == nil {
		return nil, errInvalidPreparedProto
	}
	p.once.Do(func() {
		p.state = &preparedState{}
		p.state.body, p.state.err = proto.Marshal(p.message)
	})
	return p.state.body, p.state.err
}
