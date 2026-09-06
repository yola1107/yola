package redis

import (
	"context"
	"encoding/json"
	"time"

	"yola/locate"
)

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
	values, err := l.run(ctx, bindGateScript, gateKey(candidate.ServiceName, candidate.UID),
		ttlMillis, raw, candidate.ServiceName, candidate.UID)
	if err != nil {
		return locate.GateLease{}, nil, err
	}
	switch scriptStatus(values) {
	case statusOK:
		return decodeBindGateResult(values, candidate.ServiceName, candidate.UID)
	case statusBadLease:
		return locate.GateLease{}, nil, locate.ErrInvalidGateLease
	case statusInvalid:
		return locate.GateLease{}, nil, locate.ErrInvalidGateBinding
	default:
		return locate.GateLease{}, nil, errInvalidScriptResult
	}
}

func (l *locator) LocateGate(ctx context.Context, serviceName, uid string) (locate.GateLease, error) {
	if !locate.ValidServiceName(serviceName) || !locate.ValidUID(uid) {
		return locate.GateLease{}, locate.ErrInvalidGateBinding
	}
	values, err := l.run(ctx, locateGateScript, gateKey(serviceName, uid))
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
	values, err := l.run(ctx, renewGateLeaseScript, gateKey(expected.ServiceName, expected.UID), raw, ttlMillis)
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
	values, err := l.run(ctx, unbindGateScript, gateKey(expected.ServiceName, expected.UID), raw)
	if err != nil {
		return err
	}
	status := scriptStatus(values)
	if ackIdempotentUnbind(status) {
		return nil
	}
	if status == statusBadLease {
		return locate.ErrInvalidGateLease
	}
	return errInvalidScriptResult
}
