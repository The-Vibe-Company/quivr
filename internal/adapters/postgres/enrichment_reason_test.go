package postgres_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
)

// TestStoppedEnrichmentNamesItsPlanAndPlugin owns the enrichment stop of work
// pinned to a plugin that left the active plan (THE-816): the stop is
// accepted, the Version stays searchable, blocked in enrichment, and lists
// the reason naming the plan and the plugin version; its Receipt shows the
// code and message. A later stop with a code only never shows the old reason.
func TestStoppedEnrichmentNamesItsPlanAndPlugin(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := adapterPool(t, ctx)
	run := fmt.Sprint(time.Now().UnixNano())
	org := "adapter-enrichment-reason-" + run
	scope := corpus.Scope{Organization: org, Actions: []string{"corpora:write", "content:write", "content:read"}, Corpora: []string{"*"}}
	c, _, err := (corpus.Service{Store: postgres.Store{Pool: pool}}).Create(ctx, scope, corpus.CreateInput{Key: "pinned", Name: "Pinned"})
	if err != nil {
		t.Fatal(err)
	}
	store := contentStores(pool)
	contents := content.Service{Submissions: store, Receipts: store, RecordStore: store, Versions: store, Materialization: store, Baseline: store, Embeddings: store}
	cmd := content.Command{Key: "pinned", Source: content.Source{CorpusID: c.ID, Namespace: "enrichment", RecordKey: "pinned"}, Content: content.Text{Kind: "text", Text: "The ferry crossed the fjord."}}
	r, err := contents.Accept(ctx, scope, cmd)
	if err != nil {
		t.Fatal(err)
	}
	work, _, err := store.Work(ctx, org, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Publish(ctx, work, publication(content.Blob{Key: "fixture/t", SHA256: "text-" + run, Size: 10}, content.Blob{Key: "fixture/m", SHA256: "manifest-" + run, Size: 2})); err != nil {
		t.Fatal(err)
	}
	// The fixture blobs are not in S3: keep this Receipt away from a live worker.
	if _, err = pool.Exec(ctx, `UPDATE ingestion_outbox SET dispatched=true WHERE organization=$1`, org); err != nil {
		t.Fatal(err)
	}
	v := content.Version{ID: work.VersionID, RecordID: work.RecordID, Manifest: content.ManifestFor(cmd)}
	seg, err := wholeParts(org, v)
	if err != nil {
		t.Fatal(err)
	}
	g, err := store.Generation(ctx, org, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = contents.SaveSegmentation(ctx, org, v, seg); err != nil {
		t.Fatal(err)
	}
	if err = contents.Promote(ctx, org, seg, g); err != nil {
		t.Fatal(err)
	}

	reason := content.Diagnostic{Code: "pinned_plugin_unavailable", Message: "example.embedder@0.2.0, named by plan plan_a, could not be reached.", Retryable: true, Plan: "plan_a", Plugin: "example.embedder", PluginVersion: "0.2.0", Contribution: "ingestion"}
	if err = contents.BlockEnrichment(ctx, org, work.VersionID, reason); err != nil {
		t.Fatalf("stopping the enrichment with %+v: %v", reason, err)
	}
	stored, err := store.Version(ctx, org, work.RecordID, work.VersionID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.Availability.Searchable || stored.Processing.State != "blocked" || stored.Processing.Phase != "enrichment" {
		t.Fatalf("version %s: availability %+v, processing %+v; want searchable, blocked in enrichment", work.VersionID, stored.Availability, stored.Processing)
	}
	if len(stored.Diagnostics) != 1 || stored.Diagnostics[0] != reason {
		t.Fatalf("version %s diagnostics %+v, want %+v", work.VersionID, stored.Diagnostics, reason)
	}
	receipt, err := store.Receipt(ctx, org, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(receipt.Diagnostics) != 1 || receipt.Diagnostics[0].Code != reason.Code || receipt.Diagnostics[0].Message != reason.Message {
		t.Fatalf("receipt %s diagnostics %+v, want %s: %s", r.ID, receipt.Diagnostics, reason.Code, reason.Message)
	}

	if err = contents.EnrichmentProgress(ctx, org, work.VersionID, "blocked", "derivation_conflict"); err != nil {
		t.Fatal(err)
	}
	if stored, err = store.Version(ctx, org, work.RecordID, work.VersionID); err != nil || len(stored.Diagnostics) != 0 {
		t.Fatalf("version %s blocked by derivation_conflict: diagnostics %+v (%v), want none", work.VersionID, stored.Diagnostics, err)
	}
}
