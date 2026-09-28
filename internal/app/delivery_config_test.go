package app

import (
	"testing"
	"time"
)

func TestDeliveryConfigDefaultsOverridesAndValidation(t *testing.T) {
	policy, timeout, err := DeliveryConfig{}.parse()
	if err != nil || policy.Initial != time.Second || policy.Max != 5*time.Minute || policy.Window != 24*time.Hour || timeout != 10*time.Second {
		t.Fatalf("defaults %+v %v %v", policy, timeout, err)
	}
	policy, timeout, err = DeliveryConfig{RetryInitial: "3s", RetryMax: "6s", Window: "40s", Timeout: "5s"}.parse()
	if err != nil || policy.Initial != 3*time.Second || policy.Max != 6*time.Second || policy.Window != 40*time.Second || timeout != 5*time.Second {
		t.Fatalf("overrides %+v %v %v", policy, timeout, err)
	}
	for _, bad := range []DeliveryConfig{{RetryInitial: "0s"}, {RetryMax: "-1s"}, {Window: "soon"}, {Timeout: "0"}, {RetryInitial: "10s", RetryMax: "5s"}} {
		if _, _, err = bad.parse(); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
}
