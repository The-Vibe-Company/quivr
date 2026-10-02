package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/pluginhttp"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
	"github.com/The-Vibe-Company/quivr-v2/internal/netguard"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/registry"
)

// versionParts reads the evaluated Record Version: its canonical text Parts,
// with a worker scope limited to the Version's own Corpus, and its metadata.
type versionParts struct {
	vectors  subscriptionEmbeddingReader
	content  content.Service
	metadata interface {
		RecordMetadata(ctx context.Context, org, recordID, versionID string) (monitoring.RecordMetadata, error)
	}
}

func (v versionParts) Article(ctx context.Context, org, corpusID, recordID, versionID string) (monitoring.Article, error) {
	version, err := v.content.Version(ctx, corpus.Scope{Organization: org, Actions: []string{"content:read"}, Corpora: []string{corpusID}}, recordID, versionID)
	if err != nil {
		return monitoring.Article{}, err
	}
	parts := make([]monitoring.Part, 0, len(version.Manifest.Parts))
	for _, p := range version.Manifest.Parts {
		if p.Content.Kind == "text" {
			parts = append(parts, monitoring.Part{Key: p.Key, Role: p.Role, Text: p.Content.Text})
		}
	}
	metadata, err := v.metadata.RecordMetadata(ctx, org, recordID, versionID)
	if err != nil {
		return monitoring.Article{}, err
	}
	return monitoring.Article{Parts: parts, Metadata: metadata}, nil
}

// loadPins validates every startup pin: `plugin` first, then `plugins`.
// migrate pins nothing.
func (cfg Config) loadPins(command string) (*plugins.PinSet, error) {
	var configs []plugins.PinConfig
	if cfg.Plugin != nil {
		configs = append(configs, *cfg.Plugin)
	}
	configs = append(configs, cfg.Plugins...)
	if len(configs) == 0 || command == "migrate" {
		return nil, nil
	}
	pins, err := plugins.LoadPins(configs)
	if err != nil {
		return nil, err
	}
	_, err = pins.RetrievalProfiles(cfg.Retrieval.Profiles)
	return pins, err
}

// migrationPins loads the pins for `quivr migrate`, which registers the
// ingestion plugin's spaces. A pin migrate cannot load registers nothing:
// api and worker refuse it at their own startup.
func (cfg Config) migrationPins() *plugins.PinSet {
	pins, err := cfg.loadPins("api")
	if err != nil {
		return nil
	}
	return pins
}

// planEvaluators installs the subscription evaluators of a plan's plugins,
// which new Subscription Versions pin, and the fixture evaluator only where
// the deployment enables it for tests. Every other alert-rule version a plan
// named, or the process installed before (previous), stays installed for the
// Subscription Versions that pin it, until an operator migrates them
// (THE-805); the latest plan naming a version gives its registration. A
// registry that cannot be read keeps the previous ones and returns the error.
func (cfg Config) planEvaluators(ctx context.Context, store registry.Store, set *plugins.PinSet, previous monitoring.PlanEvaluators) (monitoring.PlanEvaluators, error) {
	served := monitoring.Evaluators{}
	if cfg.MonitoringFixtureEvaluator {
		served = monitoring.FixtureEvaluators()
	}
	for _, pin := range set.Evaluators() {
		served[plugins.EvaluatorKey(pin.Manifest.ID, pin.Manifest.Version)] = pluginhttp.Evaluator{Pin: pin}
	}
	retained := monitoring.Evaluators{}
	for _, earlier := range []monitoring.Evaluators{previous.Retained, previous.Served} {
		for key, port := range earlier {
			retained[key] = port
		}
	}
	read, cancel := context.WithTimeout(ctx, 5*time.Second)
	named, err := store.EvaluatorRegistrations(read)
	cancel()
	for _, r := range named {
		pin, err := r.Pin()
		if err != nil || pin.Manifest.Contributions.Subscription == nil {
			slog.Error("an earlier alert-rule version cannot be loaded; the Subscription Versions pinning it wait", "plugin", r.PluginID, "version", r.Version, "registration", r.ID, "error", err)
			continue
		}
		retained[plugins.EvaluatorKey(pin.Manifest.ID, pin.Manifest.Version)] = pluginhttp.Evaluator{Pin: pin}
	}
	for key := range served {
		delete(retained, key)
	}
	return monitoring.PlanEvaluators{Served: served, Retained: retained}, err
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
