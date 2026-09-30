package registry

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/runner"
)

// Request registers a plugin version the operator runs at Endpoint.
type Request struct {
	// Key is the idempotency key of the request.
	Key string
	// Manifest is the exact quivr-plugin.yaml the plugin was built from: its
	// digest must be the one the plugin's discovery reports.
	Manifest []byte
	Endpoint string
	// Configuration, Routes, Kinds and Spaces install it, as a pin does.
	Configuration json.RawMessage
	Routes        []plugins.RouteConfig
	Kinds         []string
	Spaces        map[string]string
}

// Register validates the manifest and settings as a startup pin would, then
// records the registration, whose check the Contract Runner runs next. The
// same key replays the registration; a new key checks a rejected one again.
func (s Service) Register(ctx context.Context, scope corpus.Scope, req Request) (Registration, error) {
	if !scope.Allows(Action) {
		return Registration{}, corpus.ErrForbidden
	}
	pin, err := plugins.LoadPinManifest(req.Manifest, "manifest", plugins.PinConfig{Endpoint: req.Endpoint, Configuration: req.Configuration, Routes: req.Routes, Kinds: req.Kinds, Spaces: req.Spaces})
	if err != nil {
		return Registration{}, &IssueError{Kind: ErrInvalid, Issues: issuesOf(err, "")}
	}
	r := registrationOf(pin, StateRegistered)
	r.Origin = OriginRegistration
	stored, queued, err := s.Store.RegisterPlugin(ctx, r, req.Key)
	if err == nil && queued && s.Wake != nil {
		select {
		case s.Wake <- struct{}{}:
		default:
		}
	}
	return stored, err
}

// RunChecks checks queued registrations until ctx ends: it claims one, runs
// the Contract Runner against its endpoint, and records validated or
// rejected. A check a restart interrupted is claimed again once its lease
// expires.
func (s Service) RunChecks(ctx context.Context, interval, lease time.Duration) {
	check := s.Check
	if check == nil {
		check = RunCheck
	}
	for ctx.Err() == nil {
		r, ok, err := s.Store.ClaimCheck(ctx, lease)
		if err != nil && ctx.Err() == nil {
			slog.Warn("plugin check claim failed", "error", err)
		}
		if ok {
			runCtx, cancel := context.WithTimeout(ctx, lease)
			report := check(runCtx, r)
			cancel()
			if err := s.Store.RecordCheck(ctx, r.ID, report); err != nil {
				slog.Warn("plugin check not recorded; it runs again when its lease expires", "registration", r.ID, "error", err)
			} else {
				slog.Info("plugin checked", "registration", r.ID, "plugin", r.PluginID+"@"+r.Version, "certified", report.Certified, "passed", report.Passed, "failed", report.Failed)
			}
			continue
		}
		select {
		case <-ctx.Done():
		case <-s.Wake:
		case <-time.After(interval):
		}
	}
}

// RunCheck runs the Contract Runner in process against the registration's
// endpoint, with its manifest and its configuration.
func RunCheck(ctx context.Context, r Registration) CheckReport {
	started := time.Now().UTC()
	dir, err := os.MkdirTemp("", "quivr-plugin-check-")
	if err == nil {
		defer os.RemoveAll(dir)
		err = os.WriteFile(filepath.Join(dir, plugins.ManifestFile), r.Manifest, 0o600)
	}
	if err != nil {
		return CheckReport{CheckedAt: started, Failed: 1, Checks: []CheckResult{{ID: runner.CheckManifest, Title: "prepare the manifest", Status: string(runner.Fail), Issues: []plugins.Issue{{Code: plugins.CodeUnreadable, Message: err.Error()}}}}}
	}
	report := runner.Run(ctx, runner.Options{Dir: dir, Endpoint: r.Endpoint, Configuration: r.Settings.Configuration, StartupTimeout: 10 * time.Second})
	out := CheckReport{Certified: report.Certified, CheckedAt: started, Passed: report.Summary.Passed, Failed: report.Summary.Failed, Skipped: report.Summary.Skipped, Checks: make([]CheckResult, 0, len(report.Checks))}
	for _, c := range report.Checks {
		out.Checks = append(out.Checks, CheckResult{ID: c.ID, Title: c.Title, Contribution: c.Contribution, Status: string(c.Status), Issues: c.Issues})
	}
	return out
}
