package redis

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"yola/locate"
)

const (
	gateKeyPrefix      = "locate:gate:"
	nodeKeyPrefix      = "locate:node:"
	nodeEpochKeyPrefix = "locate:node:epoch:"
	maxTTLMillis       = int64((1<<63 - 1) / time.Millisecond)

	statusOK        int64 = 1
	statusMissing   int64 = 0
	statusConflict  int64 = -1
	statusBadLease  int64 = -2
	statusInvalid   int64 = -3
	statusMalformed int64 = -4

	leaseResultFields = 3
)

var errInvalidScriptResult = errors.New("locate redis returned an invalid result")

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

func decodeNodeEpochResult(values []any) error {
	switch scriptStatus(values) {
	case statusOK:
		return nil
	case statusMissing:
		return locate.ErrNodeEpochNotFound
	case statusConflict:
		return locate.ErrNodeEpochConflict
	default:
		return errInvalidScriptResult
	}
}

func decodeLeaseResult(values []any, serviceName, uid string) (locate.GateLease, error) {
	switch scriptStatus(values) {
	case statusOK:
		return decodeLease(values, serviceName, uid)
	case statusMissing:
		return locate.GateLease{}, locate.ErrGateNotFound
	case statusConflict:
		return locate.GateLease{}, locate.ErrGateConflict
	case statusBadLease:
		return locate.GateLease{}, locate.ErrInvalidGateLease
	default:
		return locate.GateLease{}, errInvalidScriptResult
	}
}

func decodeBindGateResult(values []any, serviceName, uid string) (locate.GateLease, *locate.GateBinding, error) {
	if len(values) != leaseResultFields && len(values) != leaseResultFields+1 {
		return locate.GateLease{}, nil, errInvalidScriptResult
	}
	lease, err := decodeLease(values[:leaseResultFields], serviceName, uid)
	if err != nil {
		return locate.GateLease{}, nil, err
	}
	if len(values) == leaseResultFields {
		return lease, nil, nil
	}
	previousRaw, ok := values[leaseResultFields].(string)
	if !ok {
		return locate.GateLease{}, nil, errInvalidScriptResult
	}
	previous, err := decodeBinding(previousRaw, serviceName, uid)
	if err != nil {
		return locate.GateLease{}, nil, err
	}
	return lease, &previous, nil
}

func decodeLease(values []any, serviceName, uid string) (locate.GateLease, error) {
	if len(values) != leaseResultFields {
		return locate.GateLease{}, errInvalidScriptResult
	}
	raw, ok := values[1].(string)
	if !ok {
		return locate.GateLease{}, errInvalidScriptResult
	}
	ttlMillis, ok := values[2].(int64)
	if !ok || ttlMillis <= 0 || ttlMillis > maxTTLMillis {
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

func leaseMilliseconds(ttl time.Duration) (int64, error) {
	millis := ttl.Milliseconds()
	if millis <= 0 {
		return 0, locate.ErrInvalidGateTTL
	}
	return millis, nil
}

func gateKey(serviceName, uid string) string {
	return locateKey(gateKeyPrefix, serviceName, uid)
}

func nodeKey(serviceName, uid string) string {
	return locateKey(nodeKeyPrefix, serviceName, uid)
}

func nodeEpochKey(serviceName, nodeID string) string {
	return locateKey(nodeEpochKeyPrefix, serviceName, nodeID)
}

func locateKey(prefix, serviceName, uid string) string {
	key := base64.RawURLEncoding.EncodeToString([]byte(serviceName + "\x00" + uid))
	return prefix + "{" + key + "}"
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
