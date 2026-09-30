package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/operations"
	"github.com/The-Vibe-Company/quivr-v2/internal/quarantine"
	"github.com/jackc/pgx/v5/pgxpool"
)

// activeReprocessPlan makes a plan of the test's own active, as startup does
// for a deployment; the previous active plan comes back afterwards.
func activeReprocessPlan(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	plan := fmt.Sprintf("plan-reprocess-%d", time.Now().UnixNano())
	var active *string
	if err := pool.QueryRow(ctx, `SELECT (SELECT plan_id FROM active_pipeline_plan)`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO pipeline_plans(id) VALUES($1)`, plan); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO active_pipeline_plan(plan_id) VALUES($1) ON CONFLICT(singleton) DO UPDATE SET plan_id=EXCLUDED.plan_id`, plan); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		restore, args := `DELETE FROM active_pipeline_plan`, []any{}
		if active != nil {
			restore, args = `UPDATE active_pipeline_plan SET plan_id=$1`, []any{*active}
		}
		if _, err := pool.Exec(context.Background(), restore, args...); err != nil {
			t.Errorf("restore the active plan: %v", err)
		}
	})
}

// baselineFake stands for processing.Service: the baseline of a released
// Version either succeeds (the Version becomes searchable) or is refused
// with the reason given for it.
type baselineFake struct {
	w      *pluginWorld
	refuse map[string]content.Diagnostic
	runs   int
}

func (b *baselineFake) Run(ctx context.Context, org, receiptID string) error {
	b.runs++
	r, err := b.w.contents.Receipt(ctx, b.w.scope, receiptID)
	if err != nil {
		return err
	}
	if reason, ok := b.refuse[r.VersionID]; ok {
		return b.w.store.QuarantineVersion(ctx, org, r.VersionID, reason)
	}
	_, err = b.w.pool.Exec(ctx, `UPDATE record_versions SET baseline_ready=true,processing='idle' WHERE organization=$1 AND id=$2 AND NOT quarantined`, org, r.VersionID)
	return err
}

func (b *baselineFake) Enrich(context.Context, string, string) error { return nil }

// text publishes an inline text Version and returns its id.
func (w *pluginWorld) text(recordKey, text string) string {
	w.t.Helper()
	receipt, err := w.contents.Accept(w.ctx, w.scope, content.Command{Key: "text-" + recordKey, Source: content.Source{CorpusID: w.corpusID, Namespace: "docs", RecordKey: recordKey}, Content: content.Text{Kind: "text", Text: text}})
	if err != nil {
		w.t.Fatal(err)
	}
	if err = w.contents.Materialize(w.ctx, w.org, receipt.ID); err != nil {
		w.t.Fatal(err)
	}
	_, v := w.version(receipt.ID)
	return v.ID
}

// reprocessWorld drives quarantine reprocesses of the plugin world's Corpus
// through the real store, normalizer and publication.
type reprocessWorld struct {
	*pluginWorld
	admin       corpus.Scope
	reprocesses quarantine.Service
	reprocessor quarantine.Reprocessor
	baseline    *baselineFake
}

func newReprocessWorld(t *testing.T, ctx context.Context, name string) *reprocessWorld {
	w := newPluginWorld(t, ctx, name)
	activeReprocessPlan(t, ctx, w.pool)
	b := &baselineFake{w: w, refuse: map[string]content.Diagnostic{}}
	return &reprocessWorld{pluginWorld: w, baseline: b,
		admin:       corpus.Scope{Organization: w.org, Actions: []string{operations.BackfillPermission}, Corpora: []string{"*"}},
		reprocesses: quarantine.Service{Store: w.store},
		reprocessor: quarantine.Reprocessor{Store: w.store, Normalizer: w.service, Publisher: w.contents, Processor: b, Settings: quarantine.Settings{Rate: 25}}}
}

// list reads every stuck Version f keeps, by Version id.
func (w *reprocessWorld) list(f quarantine.Filter) map[string]quarantine.Entry {
	w.t.Helper()
	entries, err := w.reprocesses.List(w.ctx, w.admin, f, "", quarantine.MaxPage)
	if err != nil {
		w.t.Fatal(err)
	}
	out := map[string]quarantine.Entry{}
	for _, e := range entries {
		out[e.VersionID] = e
	}
	return out
}

// reprocess dry-runs, accepts and runs a reprocess of f to its end.
func (w *reprocessWorld) reprocess(key string, f quarantine.Filter) operations.Operation {
	w.t.Helper()
	f.CorpusID = w.corpusID
	if _, _, err := w.reprocesses.Request(w.ctx, w.admin, quarantine.Request{Key: key, Filter: f, DryRun: true}); err != nil {
		w.t.Fatal(err)
	}
	_, op, err := w.reprocesses.Request(w.ctx, w.admin, quarantine.Request{Key: key, Filter: f})
	if err != nil {
		w.t.Fatal(err)
	}
	return w.run(op.ID)
}

func (w *reprocessWorld) run(id string) operations.Operation {
	w.t.Helper()
	for i := 0; i < 10; i++ {
		progress, err := w.reprocessor.Step(w.ctx, w.org, id)
		if err != nil {
			w.t.Fatal(err)
		}
		if progress.Done {
			op, err := w.store.Operation(w.ctx, w.org, id)
			if err != nil {
				w.t.Fatal(err)
			}
			return op
		}
	}
	w.t.Fatal("the reprocess never finished")
	return operations.Operation{}
}

func (w *reprocessWorld) events(kind, recordID string) int {
	w.t.Helper()
	var n int
	if err := w.pool.QueryRow(w.ctx, `SELECT count(*) FROM change_events WHERE organization=$1 AND event_type=$2 AND resource_id=$3`, w.org, kind, recordID).Scan(&n); err != nil {
		w.t.Fatal(err)
	}
	return n
}

func (w *reprocessWorld) read(versionID string) content.Version {
	w.t.Helper()
	var recordID string
	if err := w.pool.QueryRow(w.ctx, `SELECT record_id FROM record_versions WHERE organization=$1 AND id=$2`, w.org, versionID).Scan(&recordID); err != nil {
		w.t.Fatal(err)
	}
	v, err := w.contents.Version(w.ctx, w.scope, recordID, versionID)
	if err != nil {
		w.t.Fatal(err)
	}
	return v
}

// TestQuarantineReprocess lists the stuck Versions of a Corpus and
// reprocesses them. A normalizer failure reprocessed while the plugin still
// fails keeps its quarantine with the new invocation; once the plugin is
// fixed it is published with the normalized Manifest and processed. Of two
// ingestion quarantines, one from before structured reasons, one recovers and
// one is refused again with its new reason, announced once.
func TestQuarantineReprocess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	w := newReprocessWorld(t, ctx, "quarantine-reprocess")
	w.start("by-record-key")

	failed := w.accept("terminal.1", "text/markdown", "")
	if err := w.service.Normalize(ctx, w.org, failed.ID); err != nil {
		t.Fatal(err)
	}
	if err := w.contents.Materialize(ctx, w.org, failed.ID); err != nil {
		t.Fatal(err)
	}
	failedReceipt, failedV := w.version(failed.ID)
	refused := w.text("refused", "Harbour notes")
	if err := w.store.QuarantineVersion(ctx, w.org, refused, content.Diagnostic{Code: "ingestion_refused", Message: "refused: no text to index", Plugin: "core.ingest", PluginVersion: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	legacy := w.text("legacy", "Title only")
	if err := w.store.BaselineProgress(ctx, w.org, legacy, "blocked", "ingestion_refused", true); err != nil {
		t.Fatal(err)
	}
	// A superseded Version can never become current: not stuck.
	older := w.accept("ok.superseded@1", "text/markdown", "1")
	_ = w.accept("ok.superseded@2", "text/markdown", "2")
	if err := w.service.Normalize(ctx, w.org, older.ID); err != nil {
		t.Fatal(err)
	}
	if err := w.contents.Materialize(ctx, w.org, older.ID); err != nil {
		t.Fatal(err)
	}

	all := w.list(quarantine.Filter{})
	if len(all) != 3 {
		t.Fatalf("stuck Versions %+v, want the normalizer failure and both ingestion quarantines", all)
	}
	if e := all[failedV.ID]; e.Stage != content.QuarantineNormalization || e.Reason.Code != "normalizer_failed" || e.Reason.Plugin != "acme.faulty" || e.ReceiptID != failed.ID || e.QuarantinedAt.IsZero() {
		t.Fatalf("normalizer failure listed as %+v", e)
	}
	if e := all[refused]; e.Stage != content.QuarantineIngestion || e.Reason.Plugin != "core.ingest" || e.Reason.PluginVersion != "1.0.0" {
		t.Fatalf("ingestion refusal listed as %+v", e)
	}
	if e := all[legacy]; e.Stage != content.QuarantineIngestion || e.Reason.Code != "ingestion_refused" || e.Reason.Plugin != "" || e.Reason.Message == "" {
		t.Fatalf("reasonless quarantine listed as %+v", e)
	}
	if got := w.list(quarantine.Filter{Plugin: "acme.faulty"}); len(got) != 1 || got[failedV.ID].VersionID == "" {
		t.Fatalf("plugin filter %+v", got)
	}
	if got := w.list(quarantine.Filter{Code: "ingestion_refused"}); len(got) != 2 || got[failedV.ID].VersionID != "" {
		t.Fatalf("code filter %+v", got)
	}
	past := time.Now().Add(-time.Hour)
	if got := w.list(quarantine.Filter{Before: &past}); len(got) != 0 {
		t.Fatalf("window before the quarantines %+v", got)
	}
	if _, err := w.reprocesses.List(ctx, corpus.Scope{Organization: w.org, Actions: w.admin.Actions, Corpora: []string{"another"}}, quarantine.Filter{CorpusID: w.corpusID}, "", 10); !errors.Is(err, corpus.ErrNotFound) {
		t.Fatalf("a Corpus outside the key: %v", err)
	}

	// A dry run is required; the key replays; another scope under it conflicts.
	again := quarantine.Request{Key: "again", Filter: quarantine.Filter{CorpusID: w.corpusID, Code: "normalizer_failed"}}
	if _, _, err := w.reprocesses.Request(ctx, w.admin, again); !errors.Is(err, quarantine.ErrDryRunRequired) {
		t.Fatalf("without a dry run: %v", err)
	}
	again.DryRun = true
	e, _, err := w.reprocesses.Request(ctx, w.admin, again)
	if err != nil || e.Versions != 1 || e.Stages[content.QuarantineNormalization] != 1 || e.Codes["normalizer_failed"] != 1 {
		t.Fatalf("dry run %+v %v", e, err)
	}
	again.DryRun = false
	_, op, err := w.reprocesses.Request(ctx, w.admin, again)
	if err != nil || op.State != operations.StateQueued || op.Counters["versions_in_scope"] != 1 || op.Reprocess == nil || op.Reprocess.PlanID == "" {
		t.Fatalf("accepted %+v %v", op, err)
	}
	if _, replay, err := w.reprocesses.Request(ctx, w.admin, again); err != nil || replay.ID != op.ID {
		t.Fatalf("replay %+v %v", replay, err)
	}
	other := again
	other.Filter.Plugin = "acme.faulty"
	if _, _, err := w.reprocesses.Request(ctx, w.admin, other); !errors.Is(err, operations.ErrConflict) {
		t.Fatalf("another scope under the key: %v", err)
	}
	busy := quarantine.Request{Key: "busy", Filter: quarantine.Filter{CorpusID: w.corpusID}, DryRun: true}
	if _, _, err := w.reprocesses.Request(ctx, w.admin, busy); err != nil {
		t.Fatal(err)
	}
	busy.DryRun = false
	if _, _, err := w.reprocesses.Request(ctx, w.admin, busy); !errors.Is(err, quarantine.ErrInProgress) {
		t.Fatalf("a second reprocess of the Corpus: %v", err)
	}

	// The normalizer still fails: the Version stays quarantined with the new invocation.
	done := w.run(op.ID)
	after := w.read(failedV.ID)
	if done.State != operations.StateSucceeded || done.Counters["versions_quarantined"] != 1 || done.Counters["versions_recovered"] != 0 {
		t.Fatalf("reprocess while the plugin fails %+v", done)
	}
	if after.Availability.State != "quarantined" || len(after.Diagnostics) != 1 || after.Diagnostics[0].Code != "normalizer_failed" || after.Diagnostics[0].InvocationID == failedV.Diagnostics[0].InvocationID {
		t.Fatalf("failed again %+v, want the new invocation instead of %s", after.Diagnostics, failedV.Diagnostics[0].InvocationID)
	}
	if n := w.events("record.quarantined", failedReceipt.RecordID); n != 1 {
		t.Fatalf("record.quarantined %d times", n)
	}

	// Fixed, it is published with the normalized Manifest and processed.
	_ = w.proc.Stop(time.Second)
	w.start("ok")
	done = w.reprocess("fixed", quarantine.Filter{Code: "normalizer_failed"})
	recovered := w.read(failedV.ID)
	if done.Counters["versions_recovered"] != 1 || recovered.Availability.State != "retrieval_ready" || len(recovered.Diagnostics) != 0 {
		t.Fatalf("after the fix %+v %+v", done, recovered)
	}
	if len(recovered.Manifest.Parts) != 1 || !strings.HasPrefix(recovered.Manifest.Parts[0].Content.Text, "echo ") || recovered.Provenance["normalization"] == nil {
		t.Fatalf("republished %+v %+v", recovered.Manifest, recovered.Provenance)
	}
	if n := w.events("record.materialized", failedReceipt.RecordID); n != 2 {
		t.Fatalf("record.materialized %d times, want the first publication and the republication", n)
	}

	// Ingestion quarantines: the reasonless one recovers, the other is refused again.
	w.baseline.refuse[refused] = content.Diagnostic{Code: "ingestion_refused", Message: "refused again: the text is too long", Plugin: "core.ingest", PluginVersion: "1.0.1"}
	done = w.reprocess("ingestion", quarantine.Filter{Code: "ingestion_refused"})
	if done.Counters["versions_recovered"] != 1 || done.Counters["versions_quarantined"] != 1 {
		t.Fatalf("ingestion reprocess %+v", done)
	}
	if v := w.read(legacy); v.Availability.State != "retrieval_ready" || v.Steps.Quarantined != nil {
		t.Fatalf("the reasonless quarantine %+v", v)
	}
	v := w.read(refused)
	if v.Availability.State != "quarantined" || v.Diagnostics[0].PluginVersion != "1.0.1" || v.Steps.Quarantined == nil {
		t.Fatalf("refused again %+v", v)
	}
	var refusedRecord string
	_ = w.pool.QueryRow(ctx, `SELECT record_id FROM record_versions WHERE organization=$1 AND id=$2`, w.org, refused).Scan(&refusedRecord)
	if n := w.events("record.quarantined", refusedRecord); n != 1 {
		t.Fatalf("record.quarantined %d times for the same code", n)
	}
	if got := w.list(quarantine.Filter{}); len(got) != 1 || got[refused].VersionID == "" {
		t.Fatalf("still stuck %+v", got)
	}
}

// TestCanceledReprocessQuarantinesAgain cancels a reprocess while a Version
// it released is being processed: the Version is quarantined again with its
// previous reason rather than left waiting for a processing nobody runs.
func TestCanceledReprocessQuarantinesAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	w := newReprocessWorld(t, ctx, "quarantine-cancel")
	stuck := w.text("stuck", "Harbour notes")
	reason := content.Diagnostic{Code: "pinned_plan_stopped", Message: "stopped", Plugin: "example.plugin", PluginVersion: "0.2.0", Plan: "plan-b"}
	if err := w.store.QuarantineVersion(ctx, w.org, stuck, reason); err != nil {
		t.Fatal(err)
	}
	r := quarantine.Request{Key: "cancel", Filter: quarantine.Filter{CorpusID: w.corpusID}, DryRun: true}
	if _, _, err := w.reprocesses.Request(ctx, w.admin, r); err != nil {
		t.Fatal(err)
	}
	r.DryRun = false
	_, op, err := w.reprocesses.Request(ctx, w.admin, r)
	if err != nil {
		t.Fatal(err)
	}
	// A step released the Version, then stopped before its baseline.
	if _, _, err = w.store.BeginReprocess(ctx, w.org, op.ID); err != nil {
		t.Fatal(err)
	}
	item, err := w.store.StartReprocessItem(ctx, w.org, op.ID)
	if err != nil || item == nil || item.Phase != quarantine.PhaseReleased {
		t.Fatalf("started %+v %v", item, err)
	}
	if v := w.read(stuck); v.Availability.State == "quarantined" {
		t.Fatalf("released %+v", v)
	}
	if _, err = w.store.CancelOperation(ctx, w.org, op.ID); err != nil {
		t.Fatal(err)
	}
	done := w.run(op.ID)
	v := w.read(stuck)
	if done.State != operations.StateCanceled || done.Counters["skipped_canceled"] != 1 || w.baseline.runs != 0 {
		t.Fatalf("canceled %+v after %d baselines", done, w.baseline.runs)
	}
	if v.Availability.State != "quarantined" || len(v.Diagnostics) != 1 || v.Diagnostics[0] != reason {
		t.Fatalf("quarantined again %+v, want %+v", v.Diagnostics, reason)
	}
	if got := w.list(quarantine.Filter{}); got[stuck].Stage != content.QuarantineIngestion {
		t.Fatalf("listed %+v", got)
	}
	var recordID string
	_ = w.pool.QueryRow(ctx, `SELECT record_id FROM record_versions WHERE organization=$1 AND id=$2`, w.org, stuck).Scan(&recordID)
	if n := w.events("record.quarantined", recordID); n != 2 {
		t.Fatalf("record.quarantined %d times, want the quarantine and the one after the release", n)
	}
	// A rerun takes what is still stuck in the same scope, and recovers it.
	rerun, err := w.store.AcceptRerun(ctx, w.org, op.ID, "rerun", []byte(`{"rerun":true}`))
	if err != nil || rerun.PreviousID != op.ID || rerun.Counters["versions_in_scope"] != 1 {
		t.Fatalf("rerun %+v %v", rerun, err)
	}
	if done := w.run(rerun.ID); done.Counters["versions_recovered"] != 1 {
		t.Fatalf("rerun %+v", done)
	}
}

// TestReprocessResumesAStartedVersionThroughAPause interrupts a step after it
// released a Version: the next step finishes that Version, even once the
// reprocess is paused, and the pause holds the next ones until it resumes.
func TestReprocessResumesAStartedVersionThroughAPause(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	w := newReprocessWorld(t, ctx, "quarantine-resume")
	for _, id := range []string{w.text("first", "Harbour notes"), w.text("second", "Tide tables")} {
		if err := w.store.QuarantineVersion(ctx, w.org, id, content.Diagnostic{Code: "ingestion_refused", Message: "refused"}); err != nil {
			t.Fatal(err)
		}
	}
	r := quarantine.Request{Key: "resume", Filter: quarantine.Filter{CorpusID: w.corpusID}, DryRun: true}
	if _, _, err := w.reprocesses.Request(ctx, w.admin, r); err != nil {
		t.Fatal(err)
	}
	r.DryRun = false
	_, op, err := w.reprocesses.Request(ctx, w.admin, r)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = w.store.BeginReprocess(ctx, w.org, op.ID); err != nil {
		t.Fatal(err)
	}
	started, err := w.store.StartReprocessItem(ctx, w.org, op.ID)
	if err != nil || started == nil {
		t.Fatalf("started %+v %v", started, err)
	}
	if _, err = w.store.PauseOperation(ctx, w.org, op.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = w.reprocessor.Step(ctx, w.org, op.ID); err != nil {
		t.Fatal(err)
	}
	progress, err := w.reprocessor.Step(ctx, w.org, op.ID)
	if err != nil || progress.Done || progress.Wait != quarantine.DefaultPoll {
		t.Fatalf("paused step %+v %v", progress, err)
	}
	if state := w.read(started.VersionID).Availability.State; w.baseline.runs != 1 || state != "retrieval_ready" {
		t.Fatalf("the started Version after %d baselines: %s", w.baseline.runs, state)
	}
	if _, err = w.store.ResumeOperation(ctx, w.org, op.ID); err != nil {
		t.Fatal(err)
	}
	if done := w.run(op.ID); done.State != operations.StateSucceeded || done.Counters["versions_recovered"] != 2 || w.baseline.runs != 2 {
		t.Fatalf("resumed %+v after %d baselines", done, w.baseline.runs)
	}
}
