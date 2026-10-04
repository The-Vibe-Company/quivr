package connectors

import (
	"context"
	"errors"
	"math"
	"net/netip"
	"time"
)

// PushPolicy belongs to the engine, separately from the kind's plugin config.
// Zero rate/burst inherit the deployment defaults; no CIDRs allows every IP.
type PushPolicy struct {
	RatePerSecond float64  `json:"rate_per_second,omitempty"`
	Burst         int      `json:"burst,omitempty"`
	AllowedCIDRs  []string `json:"allowed_cidrs,omitempty"`
}

// PushConfig supplies deployment defaults and trusted reverse-proxy networks.
type PushConfig struct {
	RatePerSecond     float64  `json:"rate_per_second"`
	Burst             int      `json:"burst"`
	IdempotencyTTL    string   `json:"idempotency_ttl"`
	TrustedProxyCIDRs []string `json:"trusted_proxy_cidrs"`
}

// Resolve validates and fills defaults without allowing protection to be disabled.
func (c PushConfig) Resolve() (PushConfig, error) {
	if c.RatePerSecond == 0 {
		c.RatePerSecond = 10
	}
	if c.Burst == 0 {
		c.Burst = 100
	}
	if c.IdempotencyTTL == "" {
		c.IdempotencyTTL = "24h"
	}
	if err := (PushPolicy{RatePerSecond: c.RatePerSecond, Burst: c.Burst, AllowedCIDRs: c.TrustedProxyCIDRs}).Validate(); err != nil {
		return c, err
	}
	ttl, err := time.ParseDuration(c.IdempotencyTTL)
	if err != nil || ttl < time.Millisecond || ttl > 7*24*time.Hour {
		return c, errors.New("push idempotency TTL must be at least 1ms and at most 7 days")
	}
	return c, nil
}

// Validate checks the instance policy before it is persisted.
func (p PushPolicy) Validate() error {
	if math.IsNaN(p.RatePerSecond) || math.IsInf(p.RatePerSecond, 0) || (p.RatePerSecond != 0 && p.RatePerSecond < 0.001) || p.RatePerSecond > 100000 || p.Burst < 0 || p.Burst > 100000 || len(p.AllowedCIDRs) > 256 {
		return errors.New("invalid push rate, burst or allowlist")
	}
	for _, raw := range p.AllowedCIDRs {
		if _, err := netip.ParsePrefix(raw); err != nil {
			return errors.New("push allowlist requires CIDRs")
		}
	}
	return nil
}

func (p PushPolicy) allowsIP(ip netip.Addr) bool {
	if len(p.AllowedCIDRs) == 0 {
		return true
	}
	for _, raw := range p.AllowedCIDRs {
		if cidr, err := netip.ParsePrefix(raw); err == nil && cidr.Contains(ip.Unmap()) {
			return true
		}
	}
	return false
}

// PushAttempt is one authorized request. The key has already been hashed so
// arbitrary source keys are never persisted or logged in clear text.
type PushAttempt struct {
	Organization, InstanceID, KeyHash string
	RatePerSecond                     float64
	Burst                             int
	TTL                               time.Duration
}

// PushProtection serializes duplicate keys, shares admission across replicas,
// and commits audit events and rollups before an answer leaves the API.
type PushProtection interface {
	ProtectPush(ctx context.Context, attempt PushAttempt, invoke func() (RelayAnswer, error)) (RelayAnswer, error)
	RecordPush(ctx context.Context, instanceID string, received bool) error
}
