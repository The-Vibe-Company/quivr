package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/pluginhttp"
	"github.com/The-Vibe-Company/quivr/internal/backfill"
	"github.com/The-Vibe-Company/quivr/internal/observability"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/plugins/registry"
)

// BackfillConfig is the deployment's backfill settings (worker for rate and
// poll, api for the cost threshold).
type BackfillConfig struct {
	// MaxCostWithoutConfirmation is the estimated cost, in US dollars, above
	// which a backfill needs confirm_cost (default 0: any priced backfill).
	MaxCostWithoutConfirmation float64 `json:"max_cost_without_confirmation"`
	// Rate is the most Versions per second a backfill processes (default 2).
	Rate float64 `json:"rate"`
	// Concurrency bounds Versions in flight (default 4; 1 restores serial work).
	Concurrency int `json:"concurrency"`
	// Poll is how often a paused backfill checks whether it was resumed or
	// canceled (Go duration, default 5s).
	Poll string `json:"poll"`
}

// settings validates the configuration.
func (c BackfillConfig) settings() (backfill.Settings, error) {
	s := backfill.Settings{Rate: c.Rate, Concurrency: c.Concurrency, MaxCostWithoutConfirmation: c.MaxCostWithoutConfirmation}
	switch {
	case c.Rate < 0:
		return s, errors.New("backfill.rate must be a positive number of Versions per second")
	case c.Concurrency < 0 || c.Concurrency > 32:
		return s, errors.New("backfill.concurrency must be between 1 and 32 (or 0 for the default 4)")
	case c.MaxCostWithoutConfirmation < 0:
		return s, errors.New("backfill.max_cost_without_confirmation must not be negative")
	}
	if c.Poll != "" {
		poll, err := time.ParseDuration(c.Poll)
		if err != nil || poll <= 0 {
			return s, errors.New("backfill.poll must be a positive duration")
		}
		s.Poll = poll
	}
	return s.WithDefaults(), nil
}

// planIngestion reads the ingestion plugin of a Pipeline Plan for backfills:
// the active plan's for a request, the pinned plan's for a step.
type planIngestion struct {
	store registry.Store
	live  *plugins.Live
}

// ActiveIngestion is the active plan's ingestion registration and the
// prices its spaces declare.
func (p planIngestion) ActiveIngestion(ctx context.Context) (backfill.Ingestion, error) {
	plan, err := p.store.ActivePlan(ctx)
	if err != nil {
		return backfill.Ingestion{}, err
	}
	return p.ingestion(ctx, registry.DefaultIngestion(plan))
}

// RegistrationIngestion selects any ingestion member of the active plan.
func (p planIngestion) RegistrationIngestion(ctx context.Context, id string) (backfill.Ingestion, error) {
	plan, err := p.store.ActivePlan(ctx)
	if err != nil {
		return backfill.Ingestion{}, err
	}
	if registry.HasIngestion(plan, id) {
		return p.ingestion(ctx, id)
	}
	return backfill.Ingestion{}, backfill.ErrRegistrationNotActive
}

func (p planIngestion) ingestion(ctx context.Context, id string) (backfill.Ingestion, error) {
	if id == "" {
		return backfill.Ingestion{}, fmt.Errorf("%w: the plan has no default ingestion plugin", backfill.ErrInvalid)
	}
	reg, err := p.store.PluginRegistration(ctx, id)
	if err != nil {
		return backfill.Ingestion{}, err
	}
	pin, err := reg.Pin()
	if err != nil {
		// A registration recorded before registrations kept their manifest:
		// the plan this process follows loaded the same plugin.
		for _, candidate := range p.live.Set().Ingestions() {
			if candidate.Registration == id {
				pin = candidate
				break
			}
		}
		if pin == nil || pin.Manifest.ID != reg.PluginID || pin.Manifest.Version != reg.Version {
			return backfill.Ingestion{}, err
		}
	}
	descriptor := (pluginhttp.Ingestor{Pin: pin}).Descriptor()
	out := backfill.Ingestion{RegistrationID: reg.ID, PluginID: descriptor.PluginID, Version: descriptor.PluginVersion, Prices: descriptor.InputPrices}
	return out, nil
}

// Ingestion names the plan the work ctx carries is pinned to (the one the
// process follows when it is not pinned) and that plan's ingestion
// registration.
func (p planIngestion) Ingestion(ctx context.Context, registrationID string) (string, string, error) {
	id := p.live.Plan()
	if w, ok := plugins.WorkOf(ctx); ok {
		id = w.Plan
	}
	if id == "" {
		return "", "", errors.New("no pipeline plan to pin the backfill to")
	}
	plan, err := p.store.PipelinePlan(ctx, id)
	if err != nil {
		return "", "", err
	}
	if registrationID == "" {
		return plan.ID, registry.DefaultIngestion(plan), nil
	}
	if registry.HasIngestion(plan, registrationID) {
		return plan.ID, registrationID, nil
	}
	return plan.ID, "", nil
}

// backfillThroughput reads recent throughput from the observability rollups:
// backfilled Versions first, else the plugin's segment_and_embed calls.
type backfillThroughput struct{ reader observability.Reader }

func (t backfillThroughput) SecondsPerVersion(ctx context.Context, org, pluginID, version string) (float64, string, error) {
	window, _ := observability.ParseWindow("24h")
	mean := func(series, key string) (float64, error) {
		report, err := t.reader.Report(ctx, org, series, window)
		if err != nil {
			return 0, err
		}
		for _, s := range report.Series {
			if s.Key == key && s.Summary.Count > 0 {
				return s.Summary.MeanMS / 1000, nil
			}
		}
		return 0, nil
	}
	seconds, err := mean(observability.SeriesStep, backfill.StepName)
	if err != nil || seconds > 0 {
		return seconds, backfill.BasisBackfills, err
	}
	seconds, err = mean(observability.SeriesPluginCall, observability.Key(pluginID, version, "segment_and_embed"))
	if err != nil || seconds > 0 {
		return seconds, backfill.BasisPlugin, err
	}
	return 0, "", nil
}
