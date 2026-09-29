package redis

import (
	"context"
	"errors"

	"github.com/redis/go-redis/v9"
)

const (
	statusOK           int64 = 1
	statusMissing      int64 = 0
	statusConflict     int64 = -1
	statusBadLease     int64 = -2
	statusInvalid      int64 = -3
	statusMalformed    int64 = -4
	statusNodeMissing  int64 = -5
	statusNodeConflict int64 = -6
)

var errInvalidScriptResult = errors.New("locate redis returned an invalid result")

// bindGateScript 返回状态码、当前绑定、TTL 和可选旧绑定。
var bindGateScript = redis.NewScript(`
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

var locateGateScript = redis.NewScript(`
local current = redis.call("GET", KEYS[1])
if not current then return {0} end
local ttl = redis.call("PTTL", KEYS[1])
if ttl <= 0 then return {-2} end
return {1, current, ttl}
`)

var renewGateLeaseScript = redis.NewScript(`
local current = redis.call("GET", KEYS[1])
if not current then return {0} end
local ttl = redis.call("PTTL", KEYS[1])
if ttl <= 0 then return {-2} end
if current ~= ARGV[1] then return {-1} end
redis.call("PEXPIRE", KEYS[1], ARGV[2])
return {1, current, redis.call("PTTL", KEYS[1])}
`)

var unbindGateScript = redis.NewScript(`
local current = redis.call("GET", KEYS[1])
if not current then return {0} end
local ttl = redis.call("PTTL", KEYS[1])
if ttl <= 0 then return {-2} end
if current ~= ARGV[1] then return {-1} end
redis.call("DEL", KEYS[1])
return {1}
`)

var bindNodeScript = redis.NewScript(`
local epoch = redis.call("GET", KEYS[1])
if not epoch then return {0} end
if epoch ~= ARGV[1] then return {-1} end
redis.call("PSETEX", KEYS[2], ARGV[3], ARGV[2])
return {1}
`)

var unbindNodeScript = redis.NewScript(`
local epoch = redis.call("GET", KEYS[1])
if not epoch then return {0} end
if epoch ~= ARGV[1] then return {-1} end
if redis.call("GET", KEYS[2]) == ARGV[2] then
  redis.call("DEL", KEYS[2])
end
return {1}
`)

var deleteIfValueMatchesScript = redis.NewScript(`
local current = redis.call("GET", KEYS[1])
if not current then return {0} end
if current ~= ARGV[1] then return {-1} end
redis.call("DEL", KEYS[1])
return {1}
`)

var renewNodeScript = redis.NewScript(`
local epoch = redis.call("GET", KEYS[1])
if not epoch then return {0} end
if epoch ~= ARGV[1] then return {-1} end
local current = redis.call("GET", KEYS[2])
if not current then return {-5} end
if current ~= ARGV[2] then return {-6} end
redis.call("PEXPIRE", KEYS[2], ARGV[3])
return {1}
`)

var renewNodeEpochScript = redis.NewScript(`
local current = redis.call("GET", KEYS[1])
if not current then return {0} end
if current ~= ARGV[1] then return {-1} end
redis.call("PEXPIRE", KEYS[1], ARGV[2])
return {1}
`)

func (l *locator) run(ctx context.Context, script *redis.Script, key string, args ...any) ([]any, error) {
	values, err := script.Run(ctx, l.client, []string{key}, args...).Slice()
	return values, locatorError(ctx, err)
}

func scriptStatus(values []any) int64 {
	if len(values) == 0 {
		return statusMalformed
	}
	status, ok := values[0].(int64)
	if !ok {
		return statusMalformed
	}
	return status
}

// ackIdempotentUnbind reports whether an unbind/unregister script status is a success.
// Missing and Conflict are treated as success so retries stay idempotent.
func ackIdempotentUnbind(status int64) bool {
	switch status {
	case statusOK, statusMissing, statusConflict:
		return true
	default:
		return false
	}
}
