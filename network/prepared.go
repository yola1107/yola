package network

import (
	"errors"
	"sync"
	"sync/atomic"

	"yola/api/protocol/v1"

	"google.golang.org/protobuf/proto"
)

var errInvalidPreparedProto = errors.New("network: invalid prepared proto")

// PreparedProto is a synchronous view of one immutable Proto during a bounded
// send batch. It must not be retained after PreparedConnection.SendPrepared
// returns, and Reset must not overlap calls for the previous message.
type PreparedProto struct {
	message *v1.Proto
	state   atomic.Pointer[preparedState]
}

type preparedState struct {
	once sync.Once
	body []byte
	err  error
}

// Reset 在上一批 SendPrepared 全部返回后复用视图，不恢复旧 message 的写入权。
// fallback 连接仍可能持有旧 Proto；新旧消息及其字段都须遵守 SendProto 的不可变契约。
func (p *PreparedProto) Reset(message *v1.Proto) {
	p.message = message
	p.state.Store(nil)
}

// Message 返回供 fallback 发送的只读 Proto，连接可按 SendProto 契约继续持有它。
func (p *PreparedProto) Message() *v1.Proto {
	if p == nil {
		return nil
	}
	return p.message
}

// Marshal returns one shared protobuf encoding. Callers must not modify it.
func (p *PreparedProto) Marshal() ([]byte, error) {
	if p == nil || p.message == nil {
		return nil, errInvalidPreparedProto
	}
	state := p.state.Load()
	if state == nil {
		candidate := new(preparedState)
		if p.state.CompareAndSwap(nil, candidate) {
			state = candidate
		} else {
			state = p.state.Load()
		}
	}
	state.once.Do(func() {
		state.body, state.err = proto.Marshal(p.message)
	})
	return state.body, state.err
}
