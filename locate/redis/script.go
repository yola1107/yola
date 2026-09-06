package redis

import "github.com/redis/go-redis/v9"

// Lua 返回值首项是 locator.go 定义的状态码；Bind 还返回当前绑定、TTL 和可选旧绑定。
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

var deleteIfValueMatchesScript = redis.NewScript(`
local current = redis.call("GET", KEYS[1])
if not current then return {0} end
if current ~= ARGV[1] then return {-1} end
redis.call("DEL", KEYS[1])
return {1}
`)

var renewNodeEpochScript = redis.NewScript(`
local current = redis.call("GET", KEYS[1])
if not current then return {0} end
if current ~= ARGV[1] then return {-1} end
redis.call("PEXPIRE", KEYS[1], ARGV[2])
return {1}
`)
