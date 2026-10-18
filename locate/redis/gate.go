package redis

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"yola/locate"

	"github.com/redis/go-redis/v9"
)

const (
	_gateKeyPrefix     = "locate:gate:"
	_maxTTLMillis      = int64((1<<63 - 1) / time.Millisecond)
	_leaseResultFields = 3
)

// _bindGateScript 返回状态码、当前绑定、TTL 和可选旧绑定。
var _bindGateScript = redis.NewScript(`
local function valid_binding(raw)
  local ok, binding = pcall(cjson.decode, raw)
  return ok and type(binding) == "table" and
    binding.service_name == ARGV[3] and binding.uid == ARGV[4] and
    type(binding.gate_id) == "string" and binding.gate_id ~= "" and
    type(binding.gate_endpoint) == "string" and binding.gate_endpoint ~= "" and
    type(binding.conn_id) == "string" and binding.conn_id ~= "" and
    type(binding.binding_token) == "string" and binding.binding_token ~= ""
end

local current = redis.call("GET", KEYS[1])
if current then
  local ttl = redis.call("PTTL", KEYS[1])
  if ttl <= 0 then return {-2} end
  if not valid_binding(current) then return {-3} end
end

redis.call("PSETEX", KEYS[1], ARGV[1], ARGV[2])
local ttl = redis.call("PTTL", KEYS[1])
if current then return {1, ARGV[2], ttl, current} end
return {1, ARGV[2], ttl}
`)

var _locateGateScript = redis.NewScript(`
local current = redis.call("GET", KEYS[1])
if not current then return {0} end
local ttl = redis.call("PTTL", KEYS[1])
if ttl <= 0 then return {-2} end
return {1, current, ttl}
`)

var _renewGateLeaseScript = redis.NewScript(`
local current = redis.call("GET", KEYS[1])
if not current then return {0} end
local ttl = redis.call("PTTL", KEYS[1])
if ttl <= 0 then return {-2} end
if current ~= ARGV[1] then return {-1} end
redis.call("PEXPIRE", KEYS[1], ARGV[2])
return {1, current, redis.call("PTTL", KEYS[1])}
`)

var _unbindGateScript = redis.NewScript(`
local current = redis.call("GET", KEYS[1])
if not current then return {0} end
local ttl = redis.call("PTTL", KEYS[1])
if ttl <= 0 then return {-2} end
if current ~= ARGV[1] then return {-1} end
redis.call("DEL", KEYS[1])
return {1}
`)

func (l *locator) BindGate(ctx context.Context, candidate locate.GateBinding, ttl time.Duration) (locate.GateLease, *locate.GateBinding, error) {
	if !locate.ValidGateBinding(candidate) {
		return locate.GateLease{}, nil, locate.ErrInvalidGateBinding
	}
	ttlMillis, err := leaseMilliseconds(ttl)
	if err != nil {
		return locate.GateLease{}, nil, err
	}
	raw, err := json.Marshal(candidate)
	if err != nil {
		return locate.GateLease{}, nil, err
	}
	values, err := l.runScript(ctx, _bindGateScript, []string{gateKey(candidate.ServiceName, candidate.UID)},
		ttlMillis, raw, candidate.ServiceName, candidate.UID)
	if err != nil {
		return locate.GateLease{}, nil, err
	}
	switch scriptStatus(values) {
	case _statusOK:
		return decodeBindGateResult(values, candidate.ServiceName, candidate.UID)
	case _statusBadLease:
		return locate.GateLease{}, nil, locate.ErrInvalidGateLease
	case _statusInvalid:
		return locate.GateLease{}, nil, locate.ErrInvalidGateBinding
	default:
		return locate.GateLease{}, nil, errInvalidScriptResult
	}
}

func (l *locator) LocateGate(ctx context.Context, serviceName, uid string) (locate.GateLease, error) {
	if !locate.ValidServiceName(serviceName) || !locate.ValidUID(uid) {
		return locate.GateLease{}, locate.ErrInvalidGateBinding
	}
	values, err := l.runScript(ctx, _locateGateScript, []string{gateKey(serviceName, uid)})
	if err != nil {
		return locate.GateLease{}, err
	}
	return decodeLeaseResult(values, serviceName, uid)
}

func (l *locator) RenewGateLease(ctx context.Context, expected locate.GateBinding, ttl time.Duration) (locate.GateLease, error) {
	if !locate.ValidGateBinding(expected) {
		return locate.GateLease{}, locate.ErrInvalidGateBinding
	}
	ttlMillis, err := leaseMilliseconds(ttl)
	if err != nil {
		return locate.GateLease{}, err
	}
	raw, err := json.Marshal(expected)
	if err != nil {
		return locate.GateLease{}, err
	}
	values, err := l.runScript(ctx, _renewGateLeaseScript, []string{gateKey(expected.ServiceName, expected.UID)}, raw, ttlMillis)
	if err != nil {
		return locate.GateLease{}, err
	}
	return decodeLeaseResult(values, expected.ServiceName, expected.UID)
}

func (l *locator) UnbindGate(ctx context.Context, expected locate.GateBinding) error {
	if !locate.ValidGateBinding(expected) {
		return locate.ErrInvalidGateBinding
	}
	raw, err := json.Marshal(expected)
	if err != nil {
		return err
	}
	values, err := l.runScript(ctx, _unbindGateScript, []string{gateKey(expected.ServiceName, expected.UID)}, raw)
	if err != nil {
		return err
	}
	status := scriptStatus(values)
	if ackIdempotentUnbind(status) {
		return nil
	}
	if status == _statusBadLease {
		return locate.ErrInvalidGateLease
	}
	return errInvalidScriptResult
}

func gateKey(serviceName, uid string) string {
	key := base64.RawURLEncoding.EncodeToString([]byte(serviceName + "\x00" + uid))
	return _gateKeyPrefix + "{" + key + "}"
}

func leaseMilliseconds(ttl time.Duration) (int64, error) {
	millis := ttl.Milliseconds()
	if millis <= 0 {
		return 0, locate.ErrInvalidGateTTL
	}
	return millis, nil
}

func decodeBindGateResult(values []any, serviceName, uid string) (locate.GateLease, *locate.GateBinding, error) {
	if len(values) != _leaseResultFields && len(values) != _leaseResultFields+1 {
		return locate.GateLease{}, nil, errInvalidScriptResult
	}
	lease, err := decodeLease(values[:_leaseResultFields], serviceName, uid)
	if err != nil {
		return locate.GateLease{}, nil, err
	}
	if len(values) == _leaseResultFields {
		return lease, nil, nil
	}
	previousRaw, ok := values[_leaseResultFields].(string)
	if !ok {
		return locate.GateLease{}, nil, errInvalidScriptResult
	}
	previous, err := decodeBinding(previousRaw, serviceName, uid)
	if err != nil {
		return locate.GateLease{}, nil, err
	}
	return lease, &previous, nil
}

func decodeLeaseResult(values []any, serviceName, uid string) (locate.GateLease, error) {
	switch scriptStatus(values) {
	case _statusOK:
		return decodeLease(values, serviceName, uid)
	case _statusMissing:
		return locate.GateLease{}, locate.ErrGateNotFound
	case _statusConflict:
		return locate.GateLease{}, locate.ErrGateConflict
	case _statusBadLease:
		return locate.GateLease{}, locate.ErrInvalidGateLease
	default:
		return locate.GateLease{}, errInvalidScriptResult
	}
}

func decodeLease(values []any, serviceName, uid string) (locate.GateLease, error) {
	if len(values) != _leaseResultFields {
		return locate.GateLease{}, errInvalidScriptResult
	}
	raw, ok := values[1].(string)
	if !ok {
		return locate.GateLease{}, errInvalidScriptResult
	}
	ttlMillis, ok := values[2].(int64)
	if !ok || ttlMillis <= 0 || ttlMillis > _maxTTLMillis {
		return locate.GateLease{}, locate.ErrInvalidGateLease
	}
	binding, err := decodeBinding(raw, serviceName, uid)
	if err != nil {
		return locate.GateLease{}, err
	}
	return locate.GateLease{Binding: binding, TTL: time.Duration(ttlMillis) * time.Millisecond}, nil
}

func decodeBinding(raw, serviceName, uid string) (locate.GateBinding, error) {
	var binding locate.GateBinding
	if err := json.Unmarshal([]byte(raw), &binding); err != nil {
		return locate.GateBinding{}, fmt.Errorf("%w: decode stored value", locate.ErrInvalidGateBinding)
	}
	if binding.ServiceName != serviceName || binding.UID != uid || !locate.ValidGateBinding(binding) {
		return locate.GateBinding{}, locate.ErrInvalidGateBinding
	}
	return binding, nil
}
