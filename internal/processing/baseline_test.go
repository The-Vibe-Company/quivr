package processing_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/logging"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/processing"
)

// Baseline owns the operator diagnostic and retry contract across routing,
// plugin derivation and indexing; dependency errors must survive its boundary.
func TestBaselineRetryPreservesFailureCause(t *testing.T) {
	provider := &plugins.PluginError{Status: 503, Code: "embedding_incomplete", Message: "private submitted error marker", Retryable: true}
	for _, tc := range []struct {
		step, kind string
		cause      error
	}{
		{"route", "database_error", baselineDatabaseError{}},
		{"derive", "plugin_error", provider},
		{"derive", "network_error", errors.Join(plugins.ErrUnavailable, &net.DNSError{Err: "private submitted error marker", IsTimeout: true})},
		{"derive", "context_deadline", errors.Join(plugins.ErrUnavailable, context.DeadlineExceeded)},
		{"index", "dependency_error", errors.New("private submitted error marker")},
	} {
		t.Run(tc.step, func(t *testing.T) {
			var logs bytes.Buffer
			previous := slog.Default()
			logger, err := logging.New(&logs, logging.Options{})
			if err != nil {
				t.Fatal(err)
			}
			slog.SetDefault(logger)
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
			err = service.Run(t.Context(), "org", "receipt")
			if !errors.Is(err, tc.cause) || !strings.Contains(err.Error(), tc.step) {
				t.Fatalf("baseline lost %s cause: got %v, want wrapped %v", tc.step, err, tc.cause)
			}
			if got := store.progress[len(store.progress)-1]; got != "retrying baseline_unavailable" {
				t.Fatalf("baseline progress = %q; want retrying baseline_unavailable", got)
			}
			var outcome map[string]any
			decoder := json.NewDecoder(&logs)
			for {
				var record map[string]any
				if err := decoder.Decode(&record); err == io.EOF {
					break
				} else if err != nil {
					t.Fatalf("invalid log record: %v", err)
				}
				if strings.Contains(fmt.Sprint(record), "private submitted error marker") {
					t.Fatalf("dependency message leaked into log: %v", record)
				}
				if record["msg"] == "processing outcome" && record["code"] == "baseline_unavailable" {
					if outcome != nil {
						t.Fatal("duplicate baseline retry outcome")
					}
					outcome = record
				}
			}
			if outcome["code"] != "baseline_unavailable" || outcome["stage"] != "baseline" || outcome["outcome"] != "retrying" || outcome["failure_step"] != tc.step || outcome["failure_kind"] != tc.kind || outcome["receipt_id"] != "receipt" || outcome["version_id"] != "version" {
				t.Fatalf("missing correlated failure diagnostic: %v", outcome)
			}
			if tc.kind == "database_error" && outcome["sqlstate"] != "08006" {
				t.Fatalf("missing database connection failure: %v", outcome)
			}
			if tc.kind == "network_error" && outcome["network_timeout"] != true {
				t.Fatalf("missing network timeout: %v", outcome)
			}
			if tc.kind == "plugin_error" {
				var got *plugins.PluginError
				if !errors.As(err, &got) || got != provider || outcome["plugin_code"] != "embedding_incomplete" || outcome["plugin_http_status"] != float64(503) || outcome["plugin_retryable"] != true {
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

// A SQLSTATE-bearing dependency exercises the database diagnostic without
// coupling this processing contract to an adapter or live database.
type baselineDatabaseError struct{}

func (baselineDatabaseError) Error() string    { return "private submitted error marker" }
func (baselineDatabaseError) SQLState() string { return "08006" }
