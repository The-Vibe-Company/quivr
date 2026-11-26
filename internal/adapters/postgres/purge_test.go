package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/app"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/operations"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/jackc/pgx/v5"
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
		if it.Kind != retrieval.PurgeGeneration || len(it.Collections) != 1 || it.Collections[0] == "" || it.NoticedAt.IsZero() {
			t.Fatalf("claimed item %+v", it)
		}
	}
	if again := claimOwn(t, f.ctx, f.store, f.org, time.Hour); len(again) != 0 {
		t.Fatalf("leased items claimed twice: %+v", again)
	}
	// An incomplete purge releases its item; a complete one is stamped once.
	if _, err := f.store.RecordPurge(f.ctx, items[0], 7, false, 1000); err != nil {
		t.Fatal(err)
	}
	for _, it := range items[1:] {
		if _, err := f.store.RecordPurge(f.ctx, it, 2, true, 1000); err != nil {
			t.Fatal(err)
		}
	}
	retry := claimOwn(t, f.ctx, f.store, f.org, time.Hour)
	if len(retry) != 1 || retry[0].GenerationID != items[0].GenerationID {
		t.Fatalf("released item not reclaimed: %+v", retry)
	}
	if _, err := f.store.RecordPurge(f.ctx, retry[0], 3, true, 1000); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.RecordPurge(f.ctx, retry[0], 99, true, 1000); err != nil { // replay
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
	generationRoute, err := f.store.Generation(ctx, f.org, f.corpusID)
	if err != nil {
		t.Fatal(err)
	}
	purger := postgres.PurgeStore{Pool: f.pool, TenantCollection: generationRoute.Collection}
	items := claimOwn(t, ctx, purger, f.org, time.Hour)
	if len(items) != 2 {
		t.Fatalf("claimed %+v", items)
	}
	for _, it := range items {
		if slices.Contains(it.Collections, generationRoute.Collection) || len(it.VersionRoutes) != 1 ||
			it.VersionRoutes[0].CorpusID != f.corpusID || it.VersionRoutes[0].Generation.ID != generationRoute.ID ||
			it.VersionRoutes[0].Generation.Collection != generationRoute.Collection {
			t.Fatalf("version purge should target the owning Corpus's serving tenant: %+v", it)
		}
		if _, err = f.store.RecordPurge(ctx, it, 2, true, 1000); err != nil {
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

// Routing is real; packed file rows represent retained coverage from an old
// worker. Deleting one corpus must preserve its neighbor and current generation.
func TestPurgeRetiresAbandonedCompactCoverage(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	pool := scratchDatabase(t, ctx)
	if err := app.BootstrapDatabase(ctx, pool, app.Config{}.DeploymentSpaces(nil)); err != nil {
		t.Fatal(err)
	}
	f := controlFixture{t: t, ctx: ctx, pool: pool, org: "purge-compact", store: contentStores(pool)}
	a, b := f.corpus("rebuilt"), f.corpus("neighbor")
	scope := corpus.Scope{Organization: f.org, Actions: []string{"content:write"}, Corpora: []string{"*"}}
	old, err := f.store.Generation(ctx, f.org, a)
	if err != nil {
		t.Fatal(err)
	}
	var segments []content.Segmentation
	for i := range 3 {
		seg := rebuildSegmentation(t, ctx, f.store, scope, a, fmt.Sprint(i))
		if err := f.store.Promote(ctx, f.org, seg, old); err != nil {
			t.Fatal(err)
		}
		segments = append(segments, seg)
	}
	neighbor := rebuildSegmentation(t, ctx, f.store, scope, b, "neighbor")
	if err := f.store.Promote(ctx, f.org, neighbor, old); err != nil {
		t.Fatal(err)
	}
	op := f.rebuild(a, "replace")
	if _, err := f.store.BeginRebuild(ctx, f.org, op.ID); err != nil {
		t.Fatal(err)
	}
	for _, seg := range segments {
		if _, err := f.store.CoverRebuild(ctx, f.org, op.ID, seg, nil); err != nil {
			t.Fatal(err)
		}
	}
	if done, err := f.store.ActivateRebuild(ctx, f.org, op.ID); err != nil || !done {
		t.Fatalf("cutover: %v %v", done, err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO storage_organizations(organization) VALUES($1);`, f.org); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO storage_spaces(space_id) VALUES($1) ON CONFLICT DO NOTHING`, old.SpaceID); err != nil {
		t.Fatal(err)
	}
	for _, seg := range append(segments, neighbor) {
		corpusID := a
		if seg.VersionID == neighbor.VersionID {
			corpusID = b
		}
		var file int64
		err := pool.QueryRow(ctx, `INSERT INTO embedding_files(organization_id,version_id,segmentation_id,space_id,corpus_id,recipe,producer,object_key,sha256,byte_length,dimensions,row_count,presence)
 SELECT o.id,$2,$3,sp.id,$4,'fixture','fixture','fixture',decode(repeat('00',32),'hex'),4,1,1,decode('01','hex') FROM storage_organizations o,storage_spaces sp WHERE o.organization=$1 AND sp.space_id=$5 RETURNING id`, f.org, seg.VersionID, seg.ID, corpusID, old.SpaceID).Scan(&file)
		if err != nil {
			t.Fatal(err)
		}
		gens := []string{old.ID}
		if corpusID == a {
			gens = append(gens, op.TargetGenerationID)
		}
		for _, gen := range gens {
			if _, err := pool.Exec(ctx, `INSERT INTO compact_embedding_coverage(organization_id,file_id,generation_id,covered) SELECT id,$2,$3,decode('01','hex') FROM storage_organizations WHERE organization=$1`, f.org, file, gen); err != nil {
				t.Fatal(err)
			}
		}
	}
	notice(t, ctx, f.store)
	backdate(t, ctx, pool, f.org)
	// Completed external purges from an earlier binary still need SQL cleanup.
	if _, err := pool.Exec(ctx, `UPDATE projection_purges SET purged_at=now() WHERE organization=$1`, f.org); err != nil {
		t.Fatal(err)
	}
	items := claimOwn(t, ctx, f.store, f.org, time.Hour)
	if len(items) == 0 {
		t.Fatal("completed external purge was not claimed for coverage cleanup")
	}

	for _, it := range items {
		done := false
		for attempt := 0; attempt < 20 && !done; attempt++ {
			remaining := func() int {
				var n int
				err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM compact_embedding_coverage cc JOIN storage_organizations o ON o.id=cc.organization_id WHERE o.organization=$1)+(SELECT count(*) FROM projection_coverage WHERE organization=$1)`, f.org).Scan(&n)
				if err != nil {
					t.Fatal(err)
				}
				return n
			}
			before := remaining()
			// A fresh adapter instance resumes the durable cursor every time.
			done, err = (postgres.PurgeStore{Pool: pool}).RecordPurge(ctx, it, 0, true, 2)
			if err != nil {
				t.Fatal(err)
			}
			if removed := before - remaining(); removed > 2 {
				t.Fatalf("one batch removed %d coverage rows; limit=2", removed)
			}
		}
		if !done {
			t.Fatal("bounded coverage purge never finished")
		}
	}

	var oldRows, liveRows, neighborRows int
	err = pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE c.generation_id=$2 AND ef.corpus_id=$4),count(*) FILTER(WHERE c.generation_id=$3),count(*) FILTER(WHERE ef.corpus_id=$5) FROM compact_embedding_coverage c JOIN embedding_files ef ON ef.id=c.file_id JOIN storage_organizations o ON o.id=c.organization_id WHERE o.organization=$1`, f.org, old.ID, op.TargetGenerationID, a, b).Scan(&oldRows, &liveRows, &neighborRows)
	if err != nil || oldRows != 0 || liveRows != 3 || neighborRows != 1 {
		t.Fatalf("coverage old=%d live=%d neighbor=%d err=%v", oldRows, liveRows, neighborRows, err)
	}
	// A delayed publisher must not recreate metadata after terminal cleanup.
	if err := (postgres.RecordStore{Pool: pool}).SaveProjectionMetadata(ctx, f.org, segments[0].VersionID, old.ID, map[string]any{"metadata.language": "en"}); err != nil {
		t.Fatal(err)
	}
	var lateMetadata int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM projection_metadata WHERE organization=$1 AND version_id=$2 AND generation_id=$3`, f.org, segments[0].VersionID, old.ID).Scan(&lateMetadata); err != nil || lateMetadata != 0 {
		t.Fatalf("late abandoned-generation metadata=%d err=%v", lateMetadata, err)
	}
	var oldProjection int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM projection_coverage pc JOIN record_versions v ON(v.organization,v.id)=(pc.organization,pc.version_id) JOIN records r ON(r.organization,r.id)=(v.organization,v.record_id) WHERE pc.organization=$1 AND pc.generation_id=$2 AND r.corpus_id=$3`, f.org, old.ID, a).Scan(&oldProjection); err != nil || oldProjection != 0 {
		t.Fatalf("old lexical coverage=%d err=%v", oldProjection, err)
	}
}

// Generation metadata cleanup owns a generation-selective keyset. It must
// advance over metadata belonging to a neighboring Corpus, resume from the
// durable cursor in a fresh store, and preserve the old tuple cursor boundary
// when the same Version appears in generations on either side of that cursor.
func TestGenerationPurgeMetadataBatchesAndLegacyCursor(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	p := scratchDatabase(t, ctx)
	if err := app.BootstrapDatabase(ctx, p, app.Config{}.DeploymentSpaces(nil)); err != nil {
		t.Fatal(err)
	}
	const org = "purge-metadata"
	if _, err := p.Exec(ctx, `INSERT INTO corpora(organization,id,request_key,canonical_request,name,retrieval) VALUES
($1,'target','target','','Target','{}'),($1,'neighbor','neighbor','','Neighbor','{}')`, org); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `INSERT INTO content_blobs(organization,blob_id,object_key,sha256,byte_length)
VALUES($1,'blob','blob','blob',1)`, org); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `INSERT INTO records(organization,id,corpus_id,namespace,record_key,current_version_id,desired_version_id)
SELECT $1,'record-'||i,CASE WHEN i=2 THEN 'neighbor' ELSE 'target' END,'source',i::text,'version-'||i,'version-'||i
FROM generate_series(1,3) i`, org); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `INSERT INTO record_versions(organization,id,record_id,slot,digest,acceptance_order,source_position,text_blob_id,manifest_blob_id,provenance,baseline_ready,processing,enrichment_state)
SELECT $1,'version-'||i,'record-'||i,'text',i::text,i,i::text,'blob','blob','{}',true,'idle','idle'
FROM generate_series(1,3) i`, org); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `INSERT INTO projection_metadata(organization,version_id,generation_id,data)
SELECT $1,'version-'||i,'target-generation','{}'::jsonb FROM generate_series(1,3) i`, org); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `INSERT INTO projection_metadata(organization,version_id,generation_id,data)
VALUES($1,'version-1','neighbor-generation','{}')`, org); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `INSERT INTO projection_purges(organization,kind,corpus_id,generation_id,version_id)
VALUES($1,'generation','target','target-generation','')`, org); err != nil {
		t.Fatal(err)
	}
	item := retrieval.PurgeItem{Kind: retrieval.PurgeGeneration, Organization: org, CorpusID: "target", GenerationID: "target-generation"}
	var done bool
	var err error
	for attempt := 0; attempt < 10 && !done; attempt++ {
		var before, after int
		if err := p.QueryRow(ctx, `SELECT count(*) FROM projection_metadata WHERE organization=$1 AND generation_id=$2`, org, item.GenerationID).Scan(&before); err != nil {
			t.Fatal(err)
		}
		done, err = (postgres.PurgeStore{Pool: p}).RecordPurge(ctx, item, 0, true, 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.QueryRow(ctx, `SELECT count(*) FROM projection_metadata WHERE organization=$1 AND generation_id=$2`, org, item.GenerationID).Scan(&after); err != nil {
			t.Fatal(err)
		}
		if before-after > 1 {
			t.Fatalf("metadata batch removed %d rows with limit=1", before-after)
		}
	}
	if !done {
		t.Fatal("generation metadata purge did not finish its bounded batches")
	}
	var targetRows, neighborRows int
	if err := p.QueryRow(ctx, `SELECT count(*) FILTER (WHERE generation_id='target-generation' AND version_id IN ('version-1','version-3')),
count(*) FILTER (WHERE generation_id='target-generation' AND version_id='version-2')
FROM projection_metadata WHERE organization=$1`, org).Scan(&targetRows, &neighborRows); err != nil {
		t.Fatal(err)
	}
	if targetRows != 0 || neighborRows != 1 {
		t.Fatalf("metadata isolation target rows=%d neighbor rows=%d", targetRows, neighborRows)
	}

	// A persisted old cursor is a tuple, not only a Version key. A target
	// generation after the cursor's generation must still process the same
	// Version; one before it was already traversed and remains untouched.
	if _, err := p.Exec(ctx, `INSERT INTO records(organization,id,corpus_id,namespace,record_key,current_version_id,desired_version_id)
VALUES($1,'boundary-record','target','source','boundary','boundary-version','boundary-version')`, org); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `INSERT INTO record_versions(organization,id,record_id,slot,digest,acceptance_order,source_position,text_blob_id,manifest_blob_id,provenance,baseline_ready,processing,enrichment_state)
VALUES($1,'boundary-version','boundary-record','text','boundary',1,'boundary','blob','blob','{}',true,'idle','idle')`, org); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `INSERT INTO projection_metadata(organization,version_id,generation_id,data) VALUES
($1,'boundary-version','generation-b','{}'),($1,'boundary-version','generation-a','{}')`, org); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `INSERT INTO projection_purges(organization,kind,corpus_id,generation_id,version_id) VALUES
($1,'generation','target','generation-b',''),($1,'generation','target','generation-a','')`, org); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `UPDATE projection_purges SET cleanup_stage=2,cleanup_cursor='["boundary-version","generation-a"]'::jsonb
WHERE organization=$1 AND kind='generation' AND generation_id='generation-b'`, org); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `UPDATE projection_purges SET cleanup_stage=2,cleanup_cursor='["boundary-version","generation-b"]'::jsonb
WHERE organization=$1 AND kind='generation' AND generation_id='generation-a'`, org); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		generation string
		wantRows   int
	}{
		{generation: "generation-b", wantRows: 0},
		{generation: "generation-a", wantRows: 1},
	} {
		item := retrieval.PurgeItem{Kind: retrieval.PurgeGeneration, Organization: org, CorpusID: "target", GenerationID: tc.generation}
		var done bool
		for attempt := 0; attempt < 3 && !done; attempt++ {
			var err error
			done, err = (postgres.PurgeStore{Pool: p}).RecordPurge(ctx, item, 0, true, 1)
			if err != nil {
				t.Fatalf("legacy cursor %s: err=%v", tc.generation, err)
			}
		}
		if !done {
			t.Fatalf("legacy cursor %s did not finish", tc.generation)
		}
		var rows int
		if err := p.QueryRow(ctx, `SELECT count(*) FROM projection_metadata WHERE organization=$1 AND version_id='boundary-version' AND generation_id=$2`, org, tc.generation).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if rows != tc.wantRows {
			t.Fatalf("legacy cursor %s left %d metadata rows, want %d", tc.generation, rows, tc.wantRows)
		}
	}
}

// A blocked purge must fail before claiming progress. The holder is acquired
// synchronously, so this exercises the transaction lock fence without a sleep
// or a timing assertion, and the persisted cursor remains resumable.
func TestRecordPurgeLockTimeoutRollsBackProgress(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	p := scratchDatabase(t, ctx)
	if err := app.BootstrapDatabase(ctx, p, app.Config{}.DeploymentSpaces(nil)); err != nil {
		t.Fatal(err)
	}
	const org = "purge-lock"
	if _, err := p.Exec(ctx, `INSERT INTO projection_purges(organization,kind,corpus_id,generation_id,version_id)
VALUES($1,'generation','corpus','generation','')`, org); err != nil {
		t.Fatal(err)
	}
	holder, err := p.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Release()
	holderTx, err := holder.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holderTx.Rollback(context.Background())
	var heldStage int
	if err := holderTx.QueryRow(ctx, `SELECT cleanup_stage FROM projection_purges
WHERE organization=$1 AND kind='generation' AND corpus_id='corpus' AND generation_id='generation' AND version_id='' FOR UPDATE`, org).Scan(&heldStage); err != nil {
		t.Fatal(err)
	}
	item := retrieval.PurgeItem{Kind: retrieval.PurgeGeneration, Organization: org, CorpusID: "corpus", GenerationID: "generation"}
	if done, err := (postgres.PurgeStore{Pool: p}).RecordPurge(ctx, item, 0, true, 1); err == nil || done {
		t.Fatalf("blocked purge returned done=%t err=%v", done, err)
	}
	var stage int
	var cursor []byte
	var unpurged bool
	if err := p.QueryRow(ctx, `SELECT cleanup_stage,cleanup_cursor,purged_at IS NULL FROM projection_purges
WHERE organization=$1 AND kind='generation' AND corpus_id='corpus' AND generation_id='generation' AND version_id=''`, org).Scan(&stage, &cursor, &unpurged); err != nil {
		t.Fatal(err)
	}
	if stage != 0 || string(cursor) != "[]" || !unpurged {
		t.Fatalf("blocked purge claimed progress: stage=%d cursor=%s unpurged=%t", stage, cursor, unpurged)
	}
}

// Cancellation while the purge row is locked must abort the transaction before
// its durable cursor or completion flag changes. The tracer coordinates on the
// production SELECT rather than sleeping for a lock timeout.
func TestRecordPurgeCancellationRollsBackProgress(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	p := scratchDatabase(t, ctx)
	if err := app.BootstrapDatabase(ctx, p, app.Config{}.DeploymentSpaces(nil)); err != nil {
		t.Fatal(err)
	}
	const org = "purge-cancel"
	if _, err := p.Exec(ctx, `INSERT INTO projection_purges(organization,kind,corpus_id,generation_id,version_id)
VALUES($1,'generation','corpus','generation','')`, org); err != nil {
		t.Fatal(err)
	}
	holder, err := p.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Release()
	holderTx, err := holder.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holderTx.Rollback(context.Background())
	var heldStage int
	if err := holderTx.QueryRow(ctx, `SELECT cleanup_stage FROM projection_purges
WHERE organization=$1 AND kind='generation' AND corpus_id='corpus' AND generation_id='generation' AND version_id='' FOR UPDATE`, org).Scan(&heldStage); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 1)
	config := p.Config()
	config.ConnConfig.Tracer = &purgeCancelTrace{started: started}
	observed, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer observed.Close()
	item := retrieval.PurgeItem{Kind: retrieval.PurgeGeneration, Organization: org, CorpusID: "corpus", GenerationID: "generation"}
	callCtx, stop := context.WithCancel(ctx)
	defer stop()
	result := make(chan struct {
		done bool
		err  error
	}, 1)
	go func() {
		done, err := (postgres.PurgeStore{Pool: observed}).RecordPurge(callCtx, item, 0, true, 1)
		result <- struct {
			done bool
			err  error
		}{done: done, err: err}
	}()
	select {
	case <-started:
		stop()
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case outcome := <-result:
		if outcome.err == nil || outcome.done {
			t.Fatalf("canceled purge returned done=%t err=%v", outcome.done, outcome.err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var stage int
	var cursor []byte
	var unpurged bool
	if err := p.QueryRow(ctx, `SELECT cleanup_stage,cleanup_cursor,purged_at IS NULL FROM projection_purges
WHERE organization=$1 AND kind='generation' AND corpus_id='corpus' AND generation_id='generation' AND version_id=''`, org).Scan(&stage, &cursor, &unpurged); err != nil {
		t.Fatal(err)
	}
	if stage != 0 || string(cursor) != "[]" || !unpurged {
		t.Fatalf("canceled purge claimed progress: stage=%d cursor=%s unpurged=%t", stage, cursor, unpurged)
	}
}

type purgeCancelTrace struct{ started chan<- struct{} }

func (t *purgeCancelTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "SELECT cleanup_stage,cleanup_cursor") {
		select {
		case t.started <- struct{}{}:
		default:
		}
	}
	return ctx
}

func (*purgeCancelTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
