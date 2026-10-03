package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/registry"
)

type evaluatorRegistrationStore struct {
	registry.Store
	registration registry.Registration
}

func (s evaluatorRegistrationStore) EvaluatorRegistrations(context.Context) ([]registry.Registration, error) {
	return []registry.Registration{s.registration}, nil
}

// Startup reports an unloadable historical evaluator only while subscriptions
// or unfinished work depend on it. This owns the diagnostic at its real source.
func TestStartupReportsOnlyNeededUnloadableEvaluators(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	for _, tc := range []struct {
		name          string
		subscriptions int
		pinnedWork    int
		servedID      string
		servedVersion string
	}{
		{name: "unused"},
		{name: "subscriptions wait", subscriptions: 3},
		{name: "pending work waits", pinnedWork: 2},
		{name: "both wait", subscriptions: 3, pinnedWork: 2},
		{name: "same version served", subscriptions: 3, pinnedWork: 2, servedID: "example.alerts", servedVersion: "0.1.0"},
		{name: "different version served", subscriptions: 3, servedID: "example.alerts", servedVersion: "0.2.0"},
		{name: "different plugin served", subscriptions: 3, servedID: "example.other", servedVersion: "0.1.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			r := registry.Registration{ID: "old-registration", PluginID: "example.alerts", Version: "0.1.0", Subscriptions: tc.subscriptions, PinnedWork: tc.pinnedWork}
			// No manifest: the persisted registration predates manifest storage.
			var set *plugins.PinSet
			if tc.servedID != "" {
				manifest := `id: %s
version: %s
compatibility: {engine: ">=0.1.0 <0.3.0", plugin_api: ">=0.10.0 <0.11.0"}
contributions:
  subscription:
    expression_schema:
      type: object
      required: [kind]
      properties: {kind: {const: keywords}}
`
				pin, err := plugins.LoadPinManifest([]byte(fmt.Sprintf(manifest, tc.servedID, tc.servedVersion)), "startup-test", plugins.PinConfig{Endpoint: "http://127.0.0.1:9900"})
				if err != nil {
					t.Fatal(err)
				}
				set, err = plugins.NewPinSet([]*plugins.Pin{pin})
				if err != nil {
					t.Fatal(err)
				}
				// Same evaluator identity, but the older manifest excludes this engine.
				r.Manifest = []byte(strings.Replace(fmt.Sprintf(manifest, r.PluginID, r.Version), "<0.3.0", "<0.2.0", 1))
				r.Endpoint = pin.Endpoint
				if _, err := r.Pin(); err == nil || !strings.Contains(err.Error(), plugins.CodeIncompatibleEngine) {
					t.Fatalf("historical registration must fail engine compatibility: %v", err)
				}
			}
			installed, err := (Config{}).planEvaluators(context.Background(), evaluatorRegistrationStore{registration: r}, set, monitoring.PlanEvaluators{})
			if err != nil || len(installed.Served) != len(set.Evaluators()) || len(installed.Retained) != 0 {
				t.Fatalf("unloadable registration must be skipped without failing startup: installed=%+v, error=%v", installed, err)
			}
			if tc.servedID != "" && installed.Served[plugins.EvaluatorKey(tc.servedID, tc.servedVersion)] == nil {
				t.Fatalf("current evaluator must stay served: %+v", installed)
			}
			if (tc.subscriptions == 0 && tc.pinnedWork == 0) || (tc.servedID == r.PluginID && tc.servedVersion == r.Version) {
				if logs.Len() != 0 {
					t.Fatalf("unused or already served registration should be quiet; got %s", logs.String())
				}
				return
			}
			var entry struct {
				Level         string `json:"level"`
				Plugin        string `json:"plugin"`
				Version       string `json:"version"`
				Registration  string `json:"registration"`
				Error         string `json:"error"`
				Subscriptions int    `json:"subscriptions"`
				PinnedWork    int    `json:"pinned_work"`
			}
			if err := json.Unmarshal(logs.Bytes(), &entry); err != nil {
				t.Fatalf("want one structured error for waiting alerts, got %q: %v", logs.String(), err)
			}
			if entry.Level != "ERROR" || entry.Plugin != r.PluginID || entry.Version != r.Version || entry.Registration != r.ID || entry.Error == "" || entry.Subscriptions != tc.subscriptions || entry.PinnedWork != tc.pinnedWork {
				t.Fatalf("error must identify the unloadable registration and waiting counts; got %+v, want subscriptions=%d pinned_work=%d", entry, tc.subscriptions, tc.pinnedWork)
			}
		})
	}
}
