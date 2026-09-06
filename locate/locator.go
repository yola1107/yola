// Package locate 定位玩家当前所在的 Gateway 连接和有状态 Node。
package locate

import (
	"context"
	"errors"
	"time"
	"unicode/utf8"
)

const maxUIDBytes = 128

var (
	ErrGateNotFound       = errors.New("gate binding not found")
	ErrGateConflict       = errors.New("gate binding conflict")
	ErrInvalidGateBinding = errors.New("invalid gate binding")
	ErrInvalidGateTTL     = errors.New("invalid gate binding TTL")
	ErrInvalidGateLease   = errors.New("invalid gate binding lease")

	ErrNodeNotFound       = errors.New("node binding not found")
	ErrInvalidNodeBinding = errors.New("invalid node binding")
	ErrNodeEpochNotFound  = errors.New("node epoch not found")
	ErrNodeEpochConflict  = errors.New("node epoch conflict")
	ErrInvalidNodeEpoch   = errors.New("invalid node epoch")
)

// GateBinding 标识一条已认证的 Gateway 客户端连接。
type GateBinding struct {
	ServiceName  string `json:"service_name"`
	UID          string `json:"uid"`
	GateID       string `json:"gate_id"`
	GateEndpoint string `json:"gate_endpoint"`
	ConnID       string `json:"conn_id"`
	BindingToken string `json:"binding_token"`
}

// GateLease 包含 Gateway 绑定及其剩余 TTL。
type GateLease struct {
	Binding GateBinding
	TTL     time.Duration
}

// Pinger 检查定位存储是否可用。
type Pinger interface {
	Ping(ctx context.Context) error
}

// GateLocator 管理玩家当前所在的 Gateway 连接绑定。
type GateLocator interface {
	// BindGate 绑定 Gateway，并返回被替换的旧绑定。
	BindGate(ctx context.Context, candidate GateBinding, ttl time.Duration) (GateLease, *GateBinding, error)
	// LocateGate 获取 Gateway 绑定及其剩余 TTL。
	LocateGate(ctx context.Context, serviceName, uid string) (GateLease, error)
	// RenewGateLease 校验当前绑定后续期。
	RenewGateLease(ctx context.Context, expected GateBinding, ttl time.Duration) (GateLease, error)
	// UnbindGate 校验当前绑定后解绑。
	UnbindGate(ctx context.Context, expected GateBinding) error
}

// NodeLocator 管理由业务生命周期显式维护的 Node 绑定。
type NodeLocator interface {
	// BindNode 绑定 Node；新绑定直接覆盖旧绑定。
	BindNode(ctx context.Context, serviceName, uid, nodeID string) error
	// LocateNode 获取当前 NodeID。
	LocateNode(ctx context.Context, serviceName, uid string) (string, error)
	// UnbindNode 仅在当前 NodeID 匹配时解绑。
	UnbindNode(ctx context.Context, serviceName, uid, nodeID string) error
	// RegisterNodeEpoch 注册 service 内的 Node 进程代次；同 ID 已存在则返回 ErrNodeEpochConflict。
	RegisterNodeEpoch(ctx context.Context, serviceName, nodeID, epoch string, ttl time.Duration) error
	// RenewNodeEpoch 校验代次后续期。
	RenewNodeEpoch(ctx context.Context, serviceName, nodeID, epoch string, ttl time.Duration) error
	// LocateNodeEpoch 获取 Node 当前进程代次。
	LocateNodeEpoch(ctx context.Context, serviceName, nodeID string) (string, error)
	// UnregisterNodeEpoch 校验代次后注销。
	UnregisterNodeEpoch(ctx context.Context, serviceName, nodeID, epoch string) error
}

// Locator 定位玩家当前所在的 Gateway 和有状态 Node。
type Locator interface {
	Pinger
	GateLocator
	NodeLocator
}

// ValidGateBinding 检查绑定是否包含完整连接身份。
func ValidGateBinding(binding GateBinding) bool {
	return ValidUID(binding.UID) && ValidServiceName(binding.ServiceName) && binding.GateID != "" &&
		binding.GateEndpoint != "" && binding.ConnID != "" && binding.BindingToken != ""
}

// ValidNodeLocation 检查 Node 位置是否包含完整定位身份。
func ValidNodeLocation(serviceName, uid, nodeID string) bool {
	return ValidUID(uid) && ValidServiceName(serviceName) && nodeID != ""
}

// ValidUID 检查 UID 是否能安全用作定位 key 和日志字段。
func ValidUID(uid string) bool {
	return len(uid) > 0 && len(uid) <= maxUIDBytes && utf8.ValidString(uid)
}

// ValidServiceName 检查 service name 是否可用于 discovery key。
func ValidServiceName(name string) bool {
	if len(name) == 0 || len(name) > 128 {
		return false
	}
	for i := range len(name) {
		c := name[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}
