package app

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
	"github.com/The-Vibe-Company/quivr-v2/internal/netguard"
)

// versionParts reads a Record Version's canonical text Parts for evaluation,
// with a worker scope limited to the Version's own Corpus.
type versionParts struct{ content content.Service }

func (v versionParts) Parts(ctx context.Context, org, corpusID, recordID, versionID string) ([]monitoring.Part, error) {
	version, err := v.content.Version(ctx, corpus.Scope{Organization: org, Actions: []string{"content:read"}, Corpora: []string{corpusID}}, recordID, versionID)
	if err != nil {
		return nil, err
	}
	parts := make([]monitoring.Part, 0, len(version.Manifest.Parts))
	for _, p := range version.Manifest.Parts {
		if p.Content.Kind == "text" {
			parts = append(parts, monitoring.Part{Key: p.Key, Role: p.Role, Text: p.Content.Text})
		}
	}
	return parts, nil
}

// DeliveryConfig overrides the webhook delivery policy with Go durations.
// Empty fields keep the accepted defaults: 1s initial retry delay, 5m cap,
// 24h delivery window and a 10s request timeout. Verification shortens them
// and reports the overrides.
type DeliveryConfig struct {
	RetryInitial string `json:"retry_initial"`
	RetryMax     string `json:"retry_max"`
	Window       string `json:"window"`
	Timeout      string `json:"timeout"`
	// AllowPrivateDestinations lets delivery reach loopback, private and
	// link-local receivers. Off by default; local and CI harnesses only.
	AllowPrivateDestinations bool `json:"allow_private_destinations"`
}

func (c DeliveryConfig) parse() (monitoring.RetryPolicy, time.Duration, error) {
	durations := make([]time.Duration, 4)
	for i, value := range []string{c.RetryInitial, c.RetryMax, c.Window, c.Timeout} {
		if value == "" {
			continue
		}
		d, err := time.ParseDuration(value)
		if err != nil || d <= 0 {
			return monitoring.RetryPolicy{}, 0, errors.New("delivery durations must be positive Go durations")
		}
		durations[i] = d
	}
	policy := monitoring.RetryPolicy{Initial: durations[0], Max: durations[1], Window: durations[2]}.WithDefaults()
	if policy.Initial > policy.Max {
		return monitoring.RetryPolicy{}, 0, errors.New("delivery retry_initial must not exceed retry_max")
	}
	timeout := durations[3]
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	return policy, timeout, nil
}

// validateDestinations resolves each destination's secret_env and checks its
// URL and signing secret. Without the private-address allowance, a host that
// is plainly not public (a literal private IP or a localhost name) stops
// startup; hostnames are judged at dial time, after DNS resolution.
func validateDestinations(destinations map[string]monitoring.Destination, allowPrivate bool) error {
	for id, d := range destinations {
		if d.SecretEnv != "" {
			d.Secret = os.Getenv(d.SecretEnv)
		}
		target, err := url.Parse(d.URL)
		_, secretErr := monitoring.ParseSecret(d.Secret)
		if id == "" || d.Organization == "" || secretErr != nil || err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
			return errors.New("invalid webhook destination configuration")
		}
		if !allowPrivate && netguard.CheckLiteral(target.Hostname()) != nil {
			return fmt.Errorf("webhook destination %q targets a private or internal address; only local test deployments may set delivery.allow_private_destinations", id)
		}
		destinations[id] = d
	}
	return nil
}
