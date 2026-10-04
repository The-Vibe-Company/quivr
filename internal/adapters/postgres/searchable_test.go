package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Fault injection here targets the PostgreSQL atomic boundary; public journeys own end-to-end publication.
func TestBaselinePromotionRollbackAndHydrationFences(t *testing.T) {
	path := os.Getenv("QUIVR_ADAPTER_CONFIG")
	if path == "" {
		t.Skip("make verify real adapters")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		DatabaseURL string `json:"database_url"`
	}
	if err = json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	scope := corpus.Scope{Organization: "adapter-promotion", Actions: []string{"corpora:write", "content:write", "content:read", "search:query"}, Corpora: []string{"*"}}
	c, _, err := (corpus.Service{Store: postgres.Store{Pool: pool}}).Create(ctx, scope, corpus.CreateInput{Key: "promotion", Name: "Promotion"})
	if err != nil {
		t.Fatal(err)
	}
	store := contentStores(pool)
	service := content.Service{Submissions: store, Receipts: store, RecordStore: store, Versions: store, Materialization: store, Baseline: store}
	cmd := content.Command{Key: "guarded", Source: content.Source{CorpusID: c.ID, Namespace: "adapter", RecordKey: "guarded"}, Content: content.Text{Kind: "text", Text: "Atomic baseline"}}
	r, err := service.Accept(ctx, scope, cmd)
	if err != nil {
		t.Fatal(err)
	}
	work, _, err := store.Work(ctx, scope.Organization, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Publish(ctx, work, publication(content.Blob{Key: "fixture/text", SHA256: "guard-text", Size: 15}, content.Blob{Key: "fixture/manifest", SHA256: "guard-manifest", Size: 2})); err != nil {
		t.Fatal(err)
	}
	v := content.Version{ID: work.VersionID, RecordID: work.RecordID, Manifest: content.ManifestFor(cmd)}
	seg, err := wholeParts(scope.Organization, v)
	if err != nil {
		t.Fatal(err)
	}
	if err = service.SaveSegmentation(ctx, scope.Organization, v, seg); err != nil {
		t.Fatal(err)
	}
	if err = service.SaveSegmentation(ctx, scope.Organization, v, seg); err != nil {
		t.Fatal("retry changed segmentation", err)
	}
	divergent := seg
	divergent.Recipe = "other" // Engine rejects inconsistent derivation identities.
	if err = service.SaveSegmentation(ctx, scope.Organization, v, divergent); err == nil {
		t.Fatal("invalid contribution accepted")
	}
	g, err := store.Generation(ctx, scope.Organization, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `CREATE FUNCTION fail_fixture_promotion() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.organization='adapter-promotion' AND NEW.event_type='record.retrieval_ready' THEN RAISE EXCEPTION 'synthetic promotion interruption'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_fixture_promotion BEFORE INSERT ON change_events FOR EACH ROW EXECUTE FUNCTION fail_fixture_promotion()`)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(context.Background(), "DROP TRIGGER IF EXISTS fail_fixture_promotion ON change_events; DROP FUNCTION IF EXISTS fail_fixture_promotion()")
	if err = service.Promote(ctx, scope.Organization, seg, g); err == nil {
		t.Fatal("failure injection absent")
	}
	record, err := service.Record(ctx, scope, work.RecordID)
	if err != nil || record.CurrentVersionID != "" {
		t.Fatal("partial current promotion", record, err)
	}
	var coverage int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM projection_coverage WHERE organization=$1`, scope.Organization).Scan(&coverage); err != nil || coverage != 0 {
		t.Fatal("partial readiness coverage", coverage, err)
	}
	_, err = pool.Exec(ctx, "DROP TRIGGER fail_fixture_promotion ON change_events; DROP FUNCTION fail_fixture_promotion()")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = service.Promote(ctx, scope.Organization, seg, g); err != nil {
			t.Fatal(err)
		}
	}
	var events int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM change_events WHERE organization=$1 AND event_type='record.retrieval_ready'`, scope.Organization).Scan(&events); err != nil || events != 1 {
		t.Fatal("retry duplicated readiness fact", events, err)
	}
	candidate := content.Candidate{SegmentID: seg.Segments[0].ID, GenerationID: g.ID}
	if _, err = hydrateOne(ctx, store, scope, candidate); err != nil {
		t.Fatal(err)
	}
	// One batch lookup fences each candidate on its own and keeps its position.
	batch, err := store.Hydrate(ctx, scope, []content.Candidate{{SegmentID: "absent", GenerationID: g.ID}, candidate, {SegmentID: candidate.SegmentID, GenerationID: "unrouted"}})
	if err != nil || len(batch) != 1 || batch[1].Segment.ID != candidate.SegmentID || batch[1].Blob.Key != "fixture/text" {
		t.Fatalf("batch hydration = %+v, %v; want only the routed candidate, at position 1, with its blob", batch, err)
	}
	inaccessible := scope
	inaccessible.Corpora = []string{"ungranted"}
	if _, err = hydrateOne(ctx, store, inaccessible, candidate); !errors.Is(err, corpus.ErrNotFound) {
		t.Fatal("unauthorized hydration", err)
	}
	if _, err = pool.Exec(ctx, `UPDATE record_versions SET quarantined=true WHERE organization=$1 AND id=$2`, scope.Organization, work.VersionID); err != nil {
		t.Fatal(err)
	}
	if _, err = hydrateOne(ctx, store, scope, candidate); !errors.Is(err, corpus.ErrNotFound) {
		t.Fatal("quarantined candidate leaked", err)
	}
	if _, err = pool.Exec(ctx, `UPDATE record_versions SET quarantined=false WHERE organization=$1 AND id=$2`, scope.Organization, work.VersionID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO tombstones VALUES($1,$2)`, scope.Organization, work.RecordID); err != nil {
		t.Fatal(err)
	}
	if _, err = hydrateOne(ctx, store, scope, candidate); !errors.Is(err, corpus.ErrNotFound) {
		t.Fatal("tombstoned candidate leaked", err)
	}
	if err = service.Promote(ctx, scope.Organization, seg, g); err != nil {
		t.Fatal(err)
	}
	if _, err = hydrateOne(ctx, store, scope, candidate); !errors.Is(err, corpus.ErrNotFound) {
		t.Fatal("late promotion resurrected withdrawn content", err)
	}
	// Withdrawal while the baseline is running keeps that work observable until
	// late promotion settles it, without publishing a retrieval-ready Version.
	cmd.Key = "withdraw-running"
	cmd.Source.RecordKey = "withdraw-running"
	wr, err := service.Accept(ctx, scope, cmd)
	if err != nil {
		t.Fatal(err)
	}
	ww, _, err := store.Work(ctx, scope.Organization, wr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Publish(ctx, ww, publication(content.Blob{Key: "fixture/text", SHA256: "guard-text", Size: 15}, content.Blob{Key: "fixture/manifest", SHA256: "guard-manifest", Size: 2})); err != nil {
		t.Fatal(err)
	}
	wv := content.Version{ID: ww.VersionID, RecordID: ww.RecordID, Manifest: content.ManifestFor(cmd)}
	wseg, err := wholeParts(scope.Organization, wv)
	if err != nil {
		t.Fatal(err)
	}
	if err = service.SaveSegmentation(ctx, scope.Organization, wv, wseg); err != nil {
		t.Fatal(err)
	}
	if err = service.BaselineProgress(ctx, scope.Organization, wv.ID, "running", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Withdraw(ctx, scope, content.Withdrawal{Key: "withdraw-running", Source: cmd.Source}); err != nil {
		t.Fatal(err)
	}
	a, p, code, err := store.VersionStatus(ctx, scope.Organization, wv.ID)
	if err != nil || p.State != "running" || p.Phase != "baseline" || a.Searchable {
		t.Fatalf("withdrawn Version %s before completion: availability=%+v processing=%+v error=%v", wv.ID, a, p, err)
	}
	// A retry's diagnostic must also disappear once indexing completes.
	if err = service.BaselineProgress(ctx, scope.Organization, wv.ID, "retrying", "baseline_unavailable", false); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = service.Promote(ctx, scope.Organization, wseg, g); err != nil {
			t.Fatal(err)
		}
	}
	a, p, code, err = store.VersionStatus(ctx, scope.Organization, wv.ID)
	if err != nil || p.State != "idle" || p.Phase != "" || code != "" || a.State != "materialized" || a.Current || a.Searchable {
		t.Fatalf("withdrawn Version %s after completion: availability=%+v processing=%+v code=%q error=%v; want idle without phase or diagnostic and no retrieval readiness", wv.ID, a, p, code, err)
	}
	// A terminal unsupported result must invalidate synchronized Record views atomically.
	cmd.Key = "quarantine"
	cmd.Source.RecordKey = "quarantine"
	qr, err := service.Accept(ctx, scope, cmd)
	if err != nil {
		t.Fatal(err)
	}
	qw, _, err := store.Work(ctx, scope.Organization, qr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Publish(ctx, qw, publication(content.Blob{Key: "fixture/text", SHA256: "guard-text", Size: 15}, content.Blob{Key: "fixture/manifest", SHA256: "guard-manifest", Size: 2})); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `CREATE FUNCTION fail_fixture_quarantine() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.organization='adapter-promotion' AND NEW.event_type='record.quarantined' THEN RAISE EXCEPTION 'synthetic quarantine interruption'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_fixture_quarantine BEFORE INSERT ON change_events FOR EACH ROW EXECUTE FUNCTION fail_fixture_quarantine()`)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(context.Background(), "DROP TRIGGER IF EXISTS fail_fixture_quarantine ON change_events; DROP FUNCTION IF EXISTS fail_fixture_quarantine()")
	if err = service.BaselineProgress(ctx, scope.Organization, qw.VersionID, "blocked", "short_text_limit", true); err == nil {
		t.Fatal("quarantine event failure not atomic")
	}
	a, _, _, err = store.VersionStatus(ctx, scope.Organization, qw.VersionID)
	if err != nil || a.State == "quarantined" {
		t.Fatal("quarantine committed without invalidation", a, err)
	}
	_, err = pool.Exec(ctx, "DROP TRIGGER fail_fixture_quarantine ON change_events; DROP FUNCTION fail_fixture_quarantine()")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = service.BaselineProgress(ctx, scope.Organization, qw.VersionID, "blocked", "short_text_limit", true); err != nil {
			t.Fatal(err)
		}
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM change_events WHERE organization=$1 AND event_type='record.quarantined'`, scope.Organization).Scan(&events); err != nil || events != 1 {
		t.Fatal("quarantine event duplicated or absent", events, err)
	}
	a, _, _, err = store.VersionStatus(ctx, scope.Organization, qw.VersionID)
	if err != nil || a.State != "quarantined" {
		t.Fatal("quarantine missing", a, err)
	}

}

// wholeParts is a plugin segmentation with one segment per text Part.
func wholeParts(org string, v content.Version) (content.Segmentation, error) {
	var in []content.SegmentInput
	for _, p := range v.Manifest.Parts {
		if p.Content.Kind == "text" && p.Content.Text != "" {
			in = append(in, content.SegmentInput{PartKey: p.Key, End: len([]rune(p.Content.Text))})
		}
	}
	return content.PluginSegmentation(org, v, "plugin:adapter.fixture@1", json.RawMessage(`{"plugin_id":"adapter.fixture"}`), in)
}

// hydrateOne looks one candidate up; it is corpus.ErrNotFound when fenced.
func hydrateOne(ctx context.Context, store fixtureContentStores, scope corpus.Scope, c content.Candidate) (content.Hydrated, error) {
	found, err := store.Hydrate(ctx, scope, []content.Candidate{c})
	if err != nil {
		return content.Hydrated{}, err
	}
	l, ok := found[0]
	if !ok {
		return content.Hydrated{}, corpus.ErrNotFound
	}
	return l.Hydrated, nil
}

// TestHydrationCostFollowsTheBatch owns hydration's cost (THE-875): a batch
// reads a few index pages per candidate, whatever its Organization's size,
// even while the planner's statistics do not count the Organization yet, as
// on a new deployment or while a backfill grows it. PostgreSQL reports the
// pages each lookup reads through auto_explain on the store's own connection.
func TestHydrationCostFollowsTheBatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := adapterPool(t, ctx)
	org := "adapter-hydration-cost-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	scope := corpus.Scope{Organization: org, Actions: []string{"corpora:write", "content:write", "content:read", "search:query"}, Corpora: []string{"*"}}
	c, _, err := (corpus.Service{Store: postgres.Store{Pool: pool}}).Create(ctx, scope, corpus.CreateInput{Key: "cost", Name: "Cost"})
	if err != nil {
		t.Fatal(err)
	}
	// Statistics taken now know nothing of the Records below.
	if _, err = pool.Exec(ctx, `ANALYZE segments, record_versions, records, version_parts, content_blobs, projection_coverage`); err != nil {
		t.Fatal(err)
	}
	store := contentStores(pool)
	service := content.Service{Submissions: store, Receipts: store, RecordStore: store, Versions: store, Materialization: store, Baseline: store}
	g, err := store.Generation(ctx, org, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	var batch []content.Candidate
	for i := range 150 {
		key := "record-" + strconv.Itoa(i)
		cmd := content.Command{Key: key, Source: content.Source{CorpusID: c.ID, Namespace: "adapter", RecordKey: key}, Content: content.Text{Kind: "text", Text: "Cost of " + key}}
		r, err := service.Accept(ctx, scope, cmd)
		if err != nil {
			t.Fatal(err)
		}
		work, _, err := store.Work(ctx, org, r.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err = store.Publish(ctx, work, publication(content.Blob{Key: "fixture/" + key, SHA256: "text-" + key, Size: 15}, content.Blob{Key: "fixture/manifest-" + key, SHA256: "manifest-" + key, Size: 2})); err != nil {
			t.Fatal(err)
		}
		v := content.Version{ID: work.VersionID, RecordID: work.RecordID, Manifest: content.ManifestFor(cmd)}
		seg, err := wholeParts(org, v)
		if err != nil {
			t.Fatal(err)
		}
		if err = service.SaveSegmentation(ctx, org, v, seg); err != nil {
			t.Fatal(err)
		}
		if err = service.Promote(ctx, org, seg, g); err != nil {
			t.Fatal(err)
		}
		if i%30 == 0 {
			batch = append(batch, content.Candidate{SegmentID: seg.Segments[0].ID, GenerationID: g.ID})
		}
	}
	cfg := pool.Config().Copy()
	cfg.MaxConns = 1
	var plans []string
	cfg.ConnConfig.OnNotice = func(_ *pgconn.PgConn, n *pgconn.Notice) { plans = append(plans, n.Message) }
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, `LOAD 'auto_explain'; SET auto_explain.log_min_duration=0; SET auto_explain.log_analyze=on; SET auto_explain.log_buffers=on; SET auto_explain.log_format=json; SET auto_explain.log_level=notice`)
		return err
	}
	explained, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer explained.Close()
	// pgx prepares the query: after five runs PostgreSQL may switch to a
	// generic plan, which must be as cheap.
	for run := range 7 {
		plans = nil
		found, err := (contentStores(explained)).Hydrate(ctx, scope, batch)
		if err != nil || len(found) != len(batch) {
			t.Fatalf("run %d: hydrated %d of %d candidates, %v", run, len(found), len(batch), err)
		}
		if len(plans) != 1 {
			t.Fatalf("run %d: %d plans reported, want the one of the batch query", run, len(plans))
		}
		var plan struct {
			Plan struct {
				SharedHitBlocks  int `json:"Shared Hit Blocks"`
				SharedReadBlocks int `json:"Shared Read Blocks"`
			} `json:"Plan"`
		}
		if err = json.Unmarshal([]byte(plans[0][strings.Index(plans[0], "{"):]), &plan); err != nil {
			t.Fatal(err)
		}
		// A lookup by primary key reads about four pages; the batch reads
		// each of its tables once per candidate.
		if pages := plan.Plan.SharedHitBlocks + plan.Plan.SharedReadBlocks; pages > 50*len(batch) {
			t.Fatalf("run %d: hydrating %d candidates of an Organization of 150 Records read %d pages, want at most %d; plan: %s", run, len(batch), pages, 50*len(batch), plans[0])
		}
	}
}
