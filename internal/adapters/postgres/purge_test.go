package postgres_test

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/operations"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/jackc/pgx/v5/pgxpool"
)

// notice records every dead item in the shared adapter database.
func notice(t *testing.T, ctx context.Context, store fixtureContentStores) {
	t.Helper()
	for batch := 0; batch < 100; batch++ {
		n, err := store.NoticePurges(ctx, 1000)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			// Zero notices can mean a batch of live candidates; stop once
			// every candidate is consumed.
			var drained bool
			if err := store.Pool.QueryRow(ctx, `SELECT NOT EXISTS(SELECT FROM projection_purge_candidates)`).Scan(&drained); err != nil {
				t.Fatal(err)
			}
			if drained {
				return
			}
		}
	}
	t.Fatal("purge discovery did not drain after 100 bounded batches")
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
// mints a new Version with that content (ADR 0003), which becomes current and
// serves the reverted text, while the purged earlier Version stays dead. If
// revert semantics ever re-point a Record to an old Version, this test fails
// and the purge must also clear that Version's coverage.
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
	// Correct r back to v1's exact bytes after v1 was purged: a new Version
	// with A's content becomes current and desired; v1 is never re-pointed.
	_, reverted := f.publish("r", "r-3", "Dépêche A")
	if reverted == v1 || reverted == v2 {
		t.Fatalf("revert reused an earlier Version: reverted %s, v1 %s, v2 %s", reverted, v1, v2)
	}
	if outcome := f.outcome("r-3"); outcome != "created" {
		t.Fatalf("revert Receipt resolved %s, want created", outcome)
	}
	if current, desired := f.pointers(record); current != reverted || desired != reverted {
		t.Fatalf("revert not applied: current %s, desired %s, want %s", current, desired, reverted)
	}
	var segment, generation string
	if err = f.pool.QueryRow(ctx, `SELECT sg.id,pc.generation_id FROM segments sg JOIN projection_coverage pc ON (pc.organization,pc.version_id)=(sg.organization,sg.version_id) WHERE sg.organization=$1 AND sg.version_id=$2`, f.org, reverted).Scan(&segment, &generation); err != nil {
		t.Fatal(err)
	}
	if h, err := hydrateOne(ctx, f.store, f.scope, content.Candidate{SegmentID: segment, GenerationID: generation}); err != nil || h.VersionID != reverted || h.TextSHA256 != content.Hash([]byte("Dépêche A")) {
		t.Fatalf("Record does not serve A after the revert: %+v %v", h, err)
	}
	for _, old := range []string{v1, v2} {
		var oldSegment string
		if err = f.pool.QueryRow(ctx, `SELECT id FROM segments WHERE organization=$1 AND version_id=$2`, f.org, old).Scan(&oldSegment); err != nil {
			t.Fatal(err)
		}
		if _, err = hydrateOne(ctx, f.store, f.scope, content.Candidate{SegmentID: oldSegment, GenerationID: generation}); !errors.Is(err, corpus.ErrNotFound) {
			t.Fatalf("a superseded Version %s hydrated: %v", old, err)
		}
	}
	// Only the superseded v2 is newly noticed: the purged v1 stays dead and
	// its item stays purged, and the withdrawn Record stays fenced.
	notice(t, ctx, f.store)
	got = noticed(t, ctx, f.pool, f.org)
	want = []string{"version:::" + v1, "version:::" + v2, "version:::" + w1}
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("noticed after revert %v, want %v", got, want)
	}
	if n := f.count(`SELECT count(*) FROM projection_purges WHERE organization=$1 AND version_id=$2 AND purged_at IS NOT NULL`, f.org, v1); n != 1 {
		t.Fatalf("purged v1 item changed: %d", n)
	}

	// Replaying the revert request is the same Receipt, not another Version.
	replay, err := f.contents.Accept(ctx, f.scope, correctionCommand(f.corpusID, "r", "r-3", "Dépêche A"))
	if err != nil || replay.VersionID != reverted || replay.Outcome != "created" {
		t.Fatalf("replayed revert %+v %v", replay, err)
	}
	// The same content under a new key while it is desired is a duplicate.
	if _, again := f.publish("r", "r-4", "Dépêche A"); again != reverted || f.outcome("r-4") != "duplicate" {
		t.Fatalf("repeat of the current content minted %s (want %s), outcome %s", again, reverted, f.outcome("r-4"))
	}
	if n := f.count(`SELECT count(*) FROM record_versions WHERE organization=$1 AND record_id=$2`, f.org, record); n != 3 {
		t.Fatalf("Versions of r: %d, want 3", n)
	}

	// Discovery can consume an obsolete Version before its first segmentation
	// lands. A later segmentation must enqueue it again, and tombstone-only
	// fences must also create candidates.
	lateCommand := correctionCommand(f.corpusID, "late", "late-1", "Late text")
	lateReceipt, err := f.contents.Accept(ctx, f.scope, lateCommand)
	if err != nil {
		t.Fatal(err)
	}
	late, _, err := f.store.Work(ctx, f.org, lateReceipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.Publish(ctx, late, publication(content.Blob{Key: "fixture/late-1", SHA256: "late-text-" + f.run, Size: 9}, content.Blob{Key: "fixture/late-manifest", SHA256: "late-manifest-" + f.run, Size: 2})); err != nil {
		t.Fatal(err)
	}
	f.publish("late", "late-2", "Replacement text")
	notice(t, ctx, f.store)
	if got := noticed(t, ctx, f.pool, f.org); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("unsegmented superseded Version was noticed: %v, want %v", got, want)
	}
	lateVersion := content.Version{ID: late.VersionID, RecordID: late.RecordID, Manifest: content.ManifestFor(lateCommand)}
	if err = f.contents.SaveSegmentation(ctx, f.org, lateVersion, wholeBodySegmentation(f.org, lateVersion)); err != nil {
		t.Fatal(err)
	}
	tombstoneRecord, tombstoneVersion := f.publish("tombstone", "tombstone-1", "Tombstone text")
	notice(t, ctx, f.store) // Consume the live candidate before its fence changes.
	if _, err = f.pool.Exec(ctx, `INSERT INTO tombstones(organization,record_id) VALUES($1,$2)`, f.org, tombstoneRecord); err != nil {
		t.Fatal(err)
	}
	if err = postgres.ObserveQueueRecords(ctx, f.pool, f.org, tombstoneRecord); err != nil {
		t.Fatal(err)
	}
	notice(t, ctx, f.store)
	want = append(want, "version:::"+late.VersionID, "version:::"+tombstoneVersion)
	sort.Strings(want)
	if got := noticed(t, ctx, f.pool, f.org); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("late segmentation and tombstone candidates = %v, want %v", got, want)
	}
}

// A revert under an explicit Source Position older than the desired Version is
// a stale replay: it converges on the earlier Version and never moves desired.
func TestStaleRevertKeepsNewerDesiredVersion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	f := newCorrectionFixture(t, ctx, "adapter-stale-revert-")
	positioned := func(key, text, position string) content.Command {
		c := correctionCommand(f.corpusID, "s", key, text)
		c.Position = position
		return c
	}
	accept := func(c content.Command) content.Work {
		t.Helper()
		r, err := f.contents.Accept(ctx, f.scope, c)
		if err != nil {
			t.Fatal(err)
		}
		w, _, err := f.store.Work(ctx, f.org, r.ID)
		if err != nil {
			t.Fatal(err)
		}
		return w
	}
	a := accept(positioned("s-1", "Dépêche A", "10"))
	b := accept(positioned("s-2", "Dépêche B", "20"))
	stale := accept(positioned("s-3", "Dépêche A", "15"))
	if stale.VersionID != a.VersionID {
		t.Fatalf("stale revert minted %s, want %s", stale.VersionID, a.VersionID)
	}
	if _, desired := f.pointers(a.RecordID); desired != b.VersionID {
		t.Fatalf("stale revert moved desired to %s, want %s", desired, b.VersionID)
	}
	// A newer position reverts: a new Version becomes desired.
	fresh := accept(positioned("s-4", "Dépêche A", "30"))
	if fresh.VersionID == a.VersionID || fresh.VersionID == b.VersionID {
		t.Fatalf("revert reused %s", fresh.VersionID)
	}
	if _, desired := f.pointers(a.RecordID); desired != fresh.VersionID {
		t.Fatalf("revert left desired at %s, want %s", desired, fresh.VersionID)
	}
}
