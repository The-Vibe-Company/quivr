package fakeplugin_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/pluginhttp"
	"github.com/The-Vibe-Company/quivr/internal/connectors"
	"github.com/The-Vibe-Company/quivr/internal/monitoring"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/plugins/devhost"
	"github.com/The-Vibe-Company/quivr/internal/plugins/devhost/fakeplugin"
	"github.com/The-Vibe-Company/quivr/internal/plugins/devhost/fakeplugin/scriptedsource"
)

func TestMain(m *testing.M) {
	fakeplugin.MaybeRun()
	os.Exit(m.Run())
}

// This owns the new wire seam: the acceptance fixture must load as a normal
// pin, and its responses must survive the real adapters' protocol checks.
// Direct fixture tests retain ownership of script/rule mechanics.
func TestFixtureProtocolCarriesScriptAndDecisions(t *testing.T) {
	path := filepath.Join(t.TempDir(), plugins.ManifestFile)
	if err := os.WriteFile(path, scriptedsource.Manifest, 0600); err != nil {
		t.Fatal(err)
	}
	process, err := devhost.Start(devhost.Options{Command: fakeplugin.Command(), Manifest: path, Env: []string{fakeplugin.EnvEnable + "=1", fakeplugin.EnvMode + "=fixture"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = process.Stop(time.Second) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = process.WaitHealthy(ctx); err != nil {
		t.Fatal(err)
	}
	set, err := plugins.LoadPins([]plugins.PinConfig{{Manifest: path, Endpoint: process.BaseURL}})
	if err != nil {
		t.Fatal(err)
	}
	source := pluginhttp.Connectors(set)[0]
	req := connectors.FetchRequest{Organization: "org_a", InstanceID: "connector_1", CorpusID: "corpus_a", Namespace: "wire", Now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Config: json.RawMessage(`{"script":[{"items":[{"record_key":"a","text":"Alpha","revision":"r1"},{"record_key":"b","withdraw":true}]}]}`)}
	page, err := source.Fetch(ctx, req)
	if err != nil || len(page.Items) != 2 || page.Items[0].Content.Text != "Alpha" || page.Items[0].Revision != "r1" || !page.Items[1].Withdraw || string(page.Checkpoint) != `{"step":1}` {
		t.Fatalf("wire page: %+v %v", page, err)
	}
	req.Checkpoint = page.Checkpoint
	if page, err = source.Fetch(ctx, req); err != nil || len(page.Items) != 0 || page.More {
		t.Fatalf("wire resume: %+v %v", page, err)
	}
	req.Credential = json.RawMessage(`{"token":"fixture-revoked-token"}`)
	var failure *connectors.Error
	if _, err = source.Fetch(ctx, req); !errors.As(err, &failure) || failure.Class != connectors.ClassAccess {
		t.Fatalf("wire credential refusal: %v", err)
	}
	evaluator := pluginhttp.Evaluator{Pin: set.Evaluators()[0]}
	batch := monitoring.Batch{Organization: "org_a", CorpusID: "corpus_a", RecordID: "record_a", VersionID: "version_a", Enriched: true,
		Article: monitoring.Article{Metadata: monitoring.RecordMetadata{Source: monitoring.SourceIdentity{Namespace: "wire", RecordKey: "a"}, AcceptedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}, Parts: []monitoring.Part{{Key: "body", Role: "body", Text: "MATCH"}}},
		Items:   []monitoring.BatchItem{{ID: "e1", Subscriptions: []monitoring.SubscriptionRef{{SubscriptionID: "sub_1", SubscriptionVersionID: "subv_1", SavedQueryID: "query_1", SavedQueryVersionID: "queryv_1"}}, Configuration: map[string]any{"decisions": map[string]any{"MATCH": "match"}}}}}
	outcomes, err := evaluator.Evaluate(ctx, batch)
	if err != nil || len(outcomes) != 1 || outcomes[0].Evaluation.Decision != monitoring.DecisionMatch || len(outcomes[0].Evaluation.PartKeys) != 1 || outcomes[0].Evaluation.PartKeys[0] != "body" || outcomes[0].Evaluation.Explanation == "" {
		t.Fatalf("wire evidence: %+v %v", outcomes, err)
	}
	batch.Items[0].Configuration = map[string]any{"decisions": map[string]any{"default": "error"}}
	if _, err = evaluator.Evaluate(ctx, batch); !errors.Is(err, monitoring.ErrEvaluation) {
		t.Fatalf("wire error became decision: %v", err)
	}
}
