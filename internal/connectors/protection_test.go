package connectors

import (
	"context"
	"errors"
	"math"
	"testing"
)

// Creation owns policy validation: malformed engine settings must be refused
// before persistence even when the plugin accepts an otherwise empty config.
func TestPushPolicyIsValidatedBeforeInstanceCreation(t *testing.T) {
	for _, policy := range []PushPolicy{
		{RatePerSecond: -1}, {RatePerSecond: 1e-20}, {RatePerSecond: math.NaN()}, {RatePerSecond: 100001}, {Burst: -1}, {Burst: 100001}, {AllowedCIDRs: []string{"192.0.2.7"}},
	} {
		store := &recordingStore{}
		svc := keylessService(t, store)
		in := fixtureInput("policy", nil)
		in.PushPolicy = &policy
		if _, err := svc.Create(context.Background(), writer, in); !errors.Is(err, ErrInvalidConfig) || Field(err) != "/push_policy" || store.calls != 0 {
			t.Fatalf("invalid policy %+v: err=%v stored=%d", policy, err, store.calls)
		}
	}
}

// Deployment defaults and validation are configuration contracts, independent
// of the adapter journey's instance overrides.
func TestPushDeploymentDefaultsCannotDisableProtection(t *testing.T) {
	cfg, err := (PushConfig{}).Resolve()
	if err != nil || cfg.RatePerSecond != 10 || cfg.Burst != 100 || cfg.IdempotencyTTL != "24h" {
		t.Fatalf("defaults: %+v %v", cfg, err)
	}
	for _, cfg := range []PushConfig{
		{RatePerSecond: -1}, {Burst: -1}, {IdempotencyTTL: "1ns"}, {IdempotencyTTL: "bad"}, {IdempotencyTTL: "-1s"}, {IdempotencyTTL: "169h"}, {TrustedProxyCIDRs: []string{"localhost"}},
	} {
		if _, err := cfg.Resolve(); err == nil {
			t.Fatalf("invalid deployment admitted: %+v", cfg)
		}
	}
}
