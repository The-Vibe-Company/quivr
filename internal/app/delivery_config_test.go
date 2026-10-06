package app

import (
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/monitoring"
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

func TestDestinationsRefusePlainlyPrivateHostsUnlessAllowed(t *testing.T) {
	const secret = "whsec_AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="
	dest := func(u string) map[string]monitoring.Destination {
		return map[string]monitoring.Destination{"hook": {Organization: "org_a", URL: u, Secret: secret}}
	}
	for _, u := range []string{"http://127.0.0.1:9/h", "https://169.254.169.254/latest", "http://[::1]:8080/", "http://10.0.0.5/h", "http://localhost:8080/h", "http://[::ffff:127.0.0.1]/"} {
		err := validateDestinations(dest(u), false)
		if err == nil || !strings.Contains(err.Error(), `"hook"`) || !strings.Contains(err.Error(), "allow_private_destinations") {
			t.Errorf("%s accepted or unclear: %v", u, err)
		}
		if err := validateDestinations(dest(u), true); err != nil {
			t.Errorf("%s refused with the allowance: %v", u, err)
		}
	}
	// Hostnames are judged at dial time, after resolution.
	for _, u := range []string{"https://hooks.example.org/q", "http://93.184.216.34/h"} {
		if err := validateDestinations(dest(u), false); err != nil {
			t.Errorf("%s refused: %v", u, err)
		}
	}
	for _, u := range []string{"ftp://hooks.example.org/", "http:///nohost", "::bad"} {
		if err := validateDestinations(dest(u), true); err == nil {
			t.Errorf("%s accepted", u)
		}
	}
}
