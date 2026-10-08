package processing_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/processing"
)

// Baseline owns the operator diagnostic and retry contract across routing,
// plugin derivation and indexing; dependency errors must survive its boundary.
func TestBaselineRetryPreservesFailureCause(t *testing.T) {
	provider := &plugins.PluginError{Status: 503, Code: "embedding_incomplete", Message: "embedding provider unavailable (HTTP 503)", Retryable: true}
	for _, tc := range []struct {
		step  string
		cause error
	}{
		{"route", errors.New("routing database unavailable")},
		{"derive", provider},
		{"index", errors.New("projection unavailable")},
	} {
		t.Run(tc.step, func(t *testing.T) {
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			store := &baselineVersion{}
			contents := content.Service{Materialization: store, Receipts: store, RecordStore: store, Versions: store, Blobs: store, Baseline: store}
			owner := baselineOwner{}
			router := generationRoute{generation: content.Generation{ID: "generation", SpaceID: "example.baseline.text@1"}}
			index := baselineIndex{}
			dependencyErr := fmt.Errorf("dependency: %w", tc.cause)
			switch tc.step {
			case "route":
				router.err = dependencyErr
			case "derive":
				owner.err = dependencyErr
			case "index":
				index.err = dependencyErr
			}
			service := processing.Service{Content: contents, Routing: router, Retrieval: index, Plugin: &processing.PluginDeriver{Content: contents, Plugin: owner}}
			err := service.Run(t.Context(), "org", "receipt")
			if !errors.Is(err, tc.cause) || !strings.Contains(err.Error(), tc.step) {
				t.Fatalf("baseline lost %s cause: got %v, want wrapped %v", tc.step, err, tc.cause)
			}
			if got := store.progress[len(store.progress)-1]; got != "retrying baseline_unavailable" {
				t.Fatalf("baseline progress = %q; want retrying baseline_unavailable", got)
			}
			var outcome map[string]any
			if err := json.Unmarshal(logs.Bytes(), &outcome); err != nil {
				t.Fatalf("invalid correlated outcome %q: %v", logs.String(), err)
			}
			if outcome["code"] != "baseline_unavailable" || outcome["stage"] != "baseline" || outcome["outcome"] != "retrying" || outcome["failure_step"] != tc.step || outcome["cause"] != dependencyErr.Error() || outcome["receipt_id"] != "receipt" || outcome["version_id"] != "version" {
				t.Fatalf("missing correlated failure diagnostic: %v", outcome)
			}
			if tc.step == "derive" {
				var got *plugins.PluginError
				if !errors.As(err, &got) || got != provider || outcome["plugin_code"] != "embedding_incomplete" || outcome["plugin_http_status"] != float64(503) {
					t.Fatalf("missing plugin error diagnostic: err=%v log=%v", err, outcome)
				}
			}
		})
	}
}

type baselineVersion struct {
	oneVersion
	content.MaterializationStore
	content.BaselineRepository
}

func (*baselineVersion) Work(context.Context, string, string) (content.Work, bool, error) {
	return content.Work{}, true, nil
}
func (s *baselineVersion) Version(ctx context.Context, org, record, version string) (content.StoredVersion, error) {
	v, err := s.oneVersion.Version(ctx, org, record, version)
	v.Availability = content.Availability{State: "processing"}
	return v, err
}
func (s *baselineVersion) BaselineProgress(_ context.Context, _, _, state, code string, _ bool) error {
	s.progress = append(s.progress, state+" "+code)
	return nil
}
func (*baselineVersion) SaveSegmentation(context.Context, string, content.Segmentation) error {
	return nil
}

type baselineOwner struct{ err error }

func (baselineOwner) Descriptor() processing.IngestionDescriptor {
	return processing.IngestionDescriptor{Recipe: "plugin:example.baseline@1", SegmentsOnly: true, VectorSpaces: map[string]content.VectorSpace{"example.baseline.text@1": {ID: "example.baseline.text@1"}}}
}
func (p baselineOwner) SegmentAndEmbed(context.Context, string, string, content.Version, []string) ([]processing.PluginSegment, error) {
	return []processing.PluginSegment{{SegmentInput: content.SegmentInput{PartKey: "body", End: 14}}}, p.err
}

type baselineIndex struct{ err error }

func (i baselineIndex) Index(context.Context, string, content.Version, content.Segmentation) error {
	return i.err
}
