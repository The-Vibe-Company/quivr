package postgres_test

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/operations"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
	"github.com/jackc/pgx/v5/pgxpool"
)

// notice records every dead item in the shared adapter database.
func notice(t *testing.T, ctx context.Context, store interface {
	NoticePurges(context.Context, int) (int, error)
}) {
	t.Helper()
	for {
		n, err := store.NoticePurges(ctx, 1000)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			return
		}
	}
}

// noticed lists an Organization's recorded purge items as kind:corpus:generation:version.
func noticed(t *testing.T, ctx context.Context, pool *pgxpool.Pool, org string) []string {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT kind||':'||corpus_id||':'||generation_id||':'||version_id FROM projection_purges WHERE organization=$1 ORDER BY 1`, org)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var s string
		if err = rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

// claimOwn claims every due item and keeps the Organization's own.
func claimOwn(t *testing.T, ctx context.Context, store retrieval.PurgeStore, org string, grace time.Duration) []retrieval.PurgeItem {
	t.Helper()
	own := []retrieval.PurgeItem{}
	for {
		items, err := store.ClaimPurges(ctx, grace, time.Minute, 1000)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) == 0 {
			return own
		}
		for _, it := range items {
			if it.Organization == org {
				own = append(own, it)
			}
		}
	}
}

func backdate(t *testing.T, ctx context.Context, pool *pgxpool.Pool, org string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `UPDATE projection_purges SET noticed_at=now()-interval '2 hours' WHERE organization=$1`, org); err != nil {
		t.Fatal(err)
	}
}

// Abandoned generations are exactly the triples no route can reach again:
// failed, canceled and replaced targets, and the default generation of a
// routed Corpus. The routed generation, in-flight targets and the default
// generation of an unrouted Corpus are never selected. Claims honour the grace
// period and leases, and a completed purge is recorded once.
func TestPurgeSelectsOnlyAbandonedGenerations(t *testing.T) {
	f, cancel := newControlFixture(t, "purge-generations")
	defer cancel()
	routedCorpus, unrouted := f.corpus("routed"), f.corpus("unrouted")
	replaced, routed := f.rebuild(routedCorpus, "replaced"), f.rebuild(routedCorpus, "routed")
	for _, op := range []string{replaced.ID, routed.ID} {
		if done, err := f.activate(op); err != nil || !done {
			t.Fatalf("activate %s: %v %v", op, done, err)
		}
	}
	failed := f.rebuild(routedCorpus, "failed")
	f.begin(failed.ID)
	if err := f.store.FailRebuild(f.ctx, f.org, failed.ID, operations.Error{Code: "embedding_artifact_unavailable", Message: "stored embedding artifact unavailable"}); err != nil {
		t.Fatal(err)
	}
	canceled := f.rebuild(unrouted, "canceled")
	if _, err := f.store.CancelOperation(f.ctx, f.org, canceled.ID); err != nil {
		t.Fatal(err)
	}
	running := f.rebuild(unrouted, "running")
	f.begin(running.ID)
	var defaultGeneration string
	if err := f.pool.QueryRow(f.ctx, `SELECT id FROM projection_generations WHERE active`).Scan(&defaultGeneration); err != nil {
		t.Fatal(err)
	}
	notice(t, f.ctx, f.store)
	want := []string{
		"generation:" + routedCorpus + ":" + defaultGeneration + ":",
		"generation:" + routedCorpus + ":" + replaced.TargetGenerationID + ":",
		"generation:" + routedCorpus + ":" + failed.TargetGenerationID + ":",
		"generation:" + unrouted + ":" + canceled.TargetGenerationID + ":",
	}
	sort.Strings(want)
	if got := noticed(t, f.ctx, f.pool, f.org); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("noticed\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	notice(t, f.ctx, f.store) // idempotent
	if got := noticed(t, f.ctx, f.pool, f.org); len(got) != len(want) {
		t.Fatalf("repeated notice changed the record: %v", got)
	}
	if items := claimOwn(t, f.ctx, f.store, f.org, time.Hour); len(items) != 0 {
		t.Fatalf("claimed within the grace period: %+v", items)
	}
	backdate(t, f.ctx, f.pool, f.org)
	items := claimOwn(t, f.ctx, f.store, f.org, time.Hour)
	if len(items) != len(want) {
		t.Fatalf("claimed %+v", items)
	}
	for _, it := range items {
		if it.Kind != retrieval.PurgeGeneration || len(it.Collections) != 1 || it.Collections[0] == "" {
			t.Fatalf("claimed item %+v", it)
		}
	}
	if again := claimOwn(t, f.ctx, f.store, f.org, time.Hour); len(again) != 0 {
		t.Fatalf("leased items claimed twice: %+v", again)
	}
	// An incomplete purge releases its item; a complete one is stamped once.
	if err := f.store.RecordPurge(f.ctx, items[0], 7, false); err != nil {
		t.Fatal(err)
	}
	for _, it := range items[1:] {
		if err := f.store.RecordPurge(f.ctx, it, 2, true); err != nil {
			t.Fatal(err)
		}
	}
	retry := claimOwn(t, f.ctx, f.store, f.org, time.Hour)
	if len(retry) != 1 || retry[0].GenerationID != items[0].GenerationID {
		t.Fatalf("released item not reclaimed: %+v", retry)
	}
	if err := f.store.RecordPurge(f.ctx, retry[0], 3, true); err != nil {
		t.Fatal(err)
	}
	if err := f.store.RecordPurge(f.ctx, retry[0], 99, true); err != nil { // replay
		t.Fatal(err)
	}
	var purged, deleted int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FILTER (WHERE purged_at IS NOT NULL),coalesce(sum(objects_deleted),0) FROM projection_purges WHERE organization=$1`, f.org).Scan(&purged, &deleted); err != nil || purged != len(want) || deleted != 10+2*(len(want)-1) {
		t.Fatalf("recorded purges %d, objects %d %v", purged, deleted, err)
	}
	// The routed generation and the in-flight target still serve or build.
	if f.routed(routedCorpus) != routed.TargetGenerationID || f.state(running.ID) != "running" {
		t.Fatal("purge selection disturbed routing or a running Operation")
	}
}

// Dead Versions are superseded, withdrawn or tombstoned ones; current and
// desired Versions never are. Regression guard for the purge's permanence
// argument: correcting a Record back to an earlier Version's exact bytes
// reuses that Version's identity without making it current again, so its
// purged objects are never needed and the Record stays served by its current
// Version. If revert semantics ever re-point a Record to an old Version, this
// test fails and the purge must also clear that Version's coverage.
func TestPurgeSelectsOnlyDeadVersionsAndSurvivesRevert(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	f := newCorrectionFixture(t, ctx, "adapter-purge-versions-")
	record, v1 := f.publish("r", "r-1", "Dépêche A")
	_, v2 := f.publish("r", "r-2", "Dépêche B")
	_, w1 := f.publish("w", "w-1", "Dépêche W")
	if _, err := f.contents.Withdraw(ctx, f.scope, content.Withdrawal{Key: "w-withdraw", Source: content.Source{CorpusID: f.corpusID, Namespace: "corrections", RecordKey: "w"}}); err != nil {
		t.Fatal(err)
	}
	// A desired Version still in flight (segmented, not yet promoted).
	pending := content.Command{Key: "p-1", Source: content.Source{CorpusID: f.corpusID, Namespace: "corrections", RecordKey: "p"}, Content: content.Text{Kind: "text", Text: "Dépêche P"}}
	_, p1 := f.publish("p", "p-0", "Dépêche P0")
	receipt, err := f.contents.Accept(ctx, f.scope, pending)
	if err != nil {
		t.Fatal(err)
	}
	work, _, err := f.store.Work(ctx, f.org, receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.Publish(ctx, work, publication(content.Blob{Key: "fixture/p-1", SHA256: "text-p1" + f.run, Size: 10}, content.Blob{Key: "fixture/m-p-1", SHA256: "manifest-p1" + f.run, Size: 2})); err != nil {
		t.Fatal(err)
	}
	pv := content.Version{ID: work.VersionID, RecordID: work.RecordID, Manifest: content.ManifestFor(pending)}
	if err = f.contents.SaveSegmentation(ctx, f.org, pv, wholeBodySegmentation(f.org, pv)); err != nil {
		t.Fatal(err)
	}
	// p0 was current until p1 became desired; it stays current until p1 is promoted.
	notice(t, ctx, f.store)
	got := noticed(t, ctx, f.pool, f.org)
	want := []string{"version:::" + v1, "version:::" + w1}
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("noticed %v, want %v (current %s, desired %s and current %s kept)", got, want, v2, work.VersionID, p1)
	}
	if _, err = f.pool.Exec(ctx, `UPDATE projection_purges SET noticed_at=now()-interval '2 hours' WHERE organization=$1`, f.org); err != nil {
		t.Fatal(err)
	}
	items := claimOwn(t, ctx, f.store, f.org, time.Hour)
	if len(items) != 2 {
		t.Fatalf("claimed %+v", items)
	}
	for _, it := range items {
		if len(it.Collections) == 0 {
			t.Fatalf("version item without collections %+v", it)
		}
		if err = f.store.RecordPurge(ctx, it, 2, true); err != nil {
			t.Fatal(err)
		}
	}
	// Correct r back to v1's exact bytes after v1 was purged.
	_, reverted := f.publish("r", "r-3", "Dépêche A")
	var current, desired string
	if err = f.pool.QueryRow(ctx, `SELECT current_version_id,desired_version_id FROM records WHERE organization=$1 AND id=$2`, f.org, record).Scan(&current, &desired); err != nil {
		t.Fatal(err)
	}
	if reverted != v1 || current != v2 || desired != v2 {
		t.Fatalf("revert re-pointed the Record: reverted %s (v1 %s), current %s, desired %s, v2 %s — the purge must now clear coverage of purged Versions", reverted, v1, current, desired, v2)
	}
	var segment, generation string
	if err = f.pool.QueryRow(ctx, `SELECT sg.id,pc.generation_id FROM segments sg JOIN projection_coverage pc ON (pc.organization,pc.version_id)=(sg.organization,sg.version_id) WHERE sg.organization=$1 AND sg.version_id=$2`, f.org, v2).Scan(&segment, &generation); err != nil {
		t.Fatal(err)
	}
	if h, _, err := f.store.Hydrate(ctx, f.scope, content.Candidate{SegmentID: segment, GenerationID: generation}); err != nil || h.VersionID != v2 {
		t.Fatalf("Record not served after the revert: %+v %v", h, err)
	}
	var oldSegment string
	if err = f.pool.QueryRow(ctx, `SELECT id FROM segments WHERE organization=$1 AND version_id=$2`, f.org, v1).Scan(&oldSegment); err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.store.Hydrate(ctx, f.scope, content.Candidate{SegmentID: oldSegment, GenerationID: generation}); !errors.Is(err, corpus.ErrNotFound) {
		t.Fatalf("a purged Version hydrated: %v", err)
	}
	// Nothing new is noticed for the purged Version, and the withdrawn Record stays fenced.
	notice(t, ctx, f.store)
	if got = noticed(t, ctx, f.pool, f.org); len(got) != 2 {
		t.Fatalf("noticed after revert %v", got)
	}
}
