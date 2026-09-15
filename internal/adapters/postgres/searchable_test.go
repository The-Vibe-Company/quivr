package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/tokenizer"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/processing"
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
		DatabaseURL string           `json:"database_url"`
		Tokenizer   tokenizer.Config `json:"tokenizer"`
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
	store := postgres.ContentStore{Pool: pool}
	service := content.Service{Repository: store, Baseline: store}
	cmd := content.Command{Key: "guarded", Source: content.Source{CorpusID: c.ID, Namespace: "adapter", RecordKey: "guarded"}, Content: content.Text{Kind: "text", Text: "Atomic baseline"}}
	r, err := service.Accept(ctx, scope, cmd)
	if err != nil {
		t.Fatal(err)
	}
	work, _, err := store.Work(ctx, scope.Organization, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Publish(ctx, work, content.Blob{Key: "fixture/text", SHA256: "guard-text", Size: 15}, content.Blob{Key: "fixture/manifest", SHA256: "guard-manifest", Size: 2}); err != nil {
		t.Fatal(err)
	}
	v := content.Version{ID: work.VersionID, RecordID: work.RecordID, Manifest: content.ManifestFor(cmd)}
	seg, err := (processing.TokenWindows{Tokenizer: tokenizer.Encoder{Config: cfg.Tokenizer}}).Process(ctx, processing.Input{Organization: scope.Organization, Version: v})
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
	g, err := store.ActiveGeneration(ctx)
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
	if _, _, err = store.Hydrate(ctx, scope, candidate); err != nil {
		t.Fatal(err)
	}
	inaccessible := scope
	inaccessible.Corpora = []string{"ungranted"}
	if _, _, err = store.Hydrate(ctx, inaccessible, candidate); !errors.Is(err, corpus.ErrNotFound) {
		t.Fatal("unauthorized hydration", err)
	}
	if _, err = pool.Exec(ctx, `UPDATE record_versions SET quarantined=true WHERE organization=$1 AND id=$2`, scope.Organization, work.VersionID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.Hydrate(ctx, scope, candidate); !errors.Is(err, corpus.ErrNotFound) {
		t.Fatal("quarantined candidate leaked", err)
	}
	if _, err = pool.Exec(ctx, `UPDATE record_versions SET quarantined=false WHERE organization=$1 AND id=$2`, scope.Organization, work.VersionID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO tombstones VALUES($1,$2)`, scope.Organization, work.RecordID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.Hydrate(ctx, scope, candidate); !errors.Is(err, corpus.ErrNotFound) {
		t.Fatal("tombstoned candidate leaked", err)
	}
	if err = service.Promote(ctx, scope.Organization, seg, g); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.Hydrate(ctx, scope, candidate); !errors.Is(err, corpus.ErrNotFound) {
		t.Fatal("late promotion resurrected withdrawn content", err)
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
	if err = store.Publish(ctx, qw, content.Blob{Key: "fixture/text", SHA256: "guard-text", Size: 15}, content.Blob{Key: "fixture/manifest", SHA256: "guard-manifest", Size: 2}); err != nil {
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
	a, _, _, err := store.VersionStatus(ctx, scope.Organization, qw.VersionID)
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
