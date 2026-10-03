package app

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/monitoring"
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
	}{
		{name: "unused"},
		{name: "subscriptions wait", subscriptions: 3},
		{name: "pending work waits", pinnedWork: 2},
		{name: "both wait", subscriptions: 3, pinnedWork: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			r := registry.Registration{ID: "old-registration", PluginID: "example.alerts", Version: "0.1.0", Subscriptions: tc.subscriptions, PinnedWork: tc.pinnedWork}
			// No manifest: the persisted registration predates manifest storage.
			installed, err := (Config{}).planEvaluators(context.Background(), evaluatorRegistrationStore{registration: r}, nil, monitoring.PlanEvaluators{})
			if err != nil || len(installed.Served) != 0 || len(installed.Retained) != 0 {
				t.Fatalf("unloadable registration must be skipped without failing startup: installed=%+v, error=%v", installed, err)
			}
			if tc.subscriptions == 0 && tc.pinnedWork == 0 {
				if logs.Len() != 0 {
					t.Fatalf("unused registration should be quiet; got %s", logs.String())
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
