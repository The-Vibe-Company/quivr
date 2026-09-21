package postgres_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Relation resolution must reuse the canonical eligibility guards; a missing,
// unready, quarantined, withdrawn or unauthorized target is indistinguishable.
func TestRelationResolutionEligibilityGuards(t *testing.T) {
	path := os.Getenv("QUIVR_ADAPTER_CONFIG")
	if path == "" {
		t.Skip("real PostgreSQL suite runs inside make verify")
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
	scope := corpus.Scope{Organization: "adapter-relations", Actions: []string{"corpora:write", "content:write", "content:read"}, Corpora: []string{"*"}}
	c, _, err := (corpus.Service{Store: postgres.Store{Pool: pool}}).Create(ctx, scope, corpus.CreateInput{Key: "relations", Name: "Relations"})
	if err != nil {
		t.Fatal(err)
	}
	store := postgres.ContentStore{Pool: pool}
	service := content.Service{Repository: store}
	cmd := content.Command{Key: "target", Source: content.Source{CorpusID: c.ID, Namespace: "adapter", RecordKey: "target"}, Content: content.Text{Kind: "text", Text: "Target content"}}
	receipt, err := service.Accept(ctx, scope, cmd)
	if err != nil {
		t.Fatal(err)
	}
	work, _, err := store.Work(ctx, scope.Organization, receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Publish(ctx, work, publication(content.Blob{Key: "fixtures/relations/text", SHA256: "relations-text", Size: 14}, content.Blob{Key: "fixtures/relations/manifest", SHA256: "relations-manifest", Size: 2})); err != nil {
		t.Fatal(err)
	}
	relation := content.Relation{Type: "illustrated_by", Target: content.Source{CorpusID: c.ID, Namespace: "adapter", RecordKey: "target"}}
	assert := func(t *testing.T, want string) {
		t.Helper()
		resolved, err := store.Resolve(ctx, scope, []content.Relation{relation})
		if err != nil {
			t.Fatal(err)
		}
		if len(resolved) != 1 || resolved[0].Status != want {
			t.Fatalf("want %s got %+v", want, resolved)
		}
		if want == "unavailable" && (resolved[0].TargetRecordID != "" || resolved[0].TargetVersionID != "") {
			t.Fatalf("unavailable relation leaked a target: %+v", resolved[0])
		}
		if want == "available" && (resolved[0].TargetRecordID != work.RecordID || resolved[0].TargetVersionID != work.VersionID) {
			t.Fatalf("available target wrong: %+v", resolved[0])
		}
	}
	assert(t, "unavailable") // Materialized but not yet current or baseline-ready.
	if _, err = pool.Exec(ctx, `UPDATE record_versions SET baseline_ready=true WHERE organization=$1 AND id=$2`, scope.Organization, work.VersionID); err != nil {
		t.Fatal(err)
	}
	assert(t, "unavailable") // Ready but not the current Version.
	if _, err = pool.Exec(ctx, `UPDATE records SET current_version_id=$3 WHERE organization=$1 AND id=$2`, scope.Organization, work.RecordID, work.VersionID); err != nil {
		t.Fatal(err)
	}
	assert(t, "available")
	if _, err = pool.Exec(ctx, `UPDATE record_versions SET quarantined=true WHERE organization=$1 AND id=$2`, scope.Organization, work.VersionID); err != nil {
		t.Fatal(err)
	}
	assert(t, "unavailable")
	if _, err = pool.Exec(ctx, `UPDATE record_versions SET quarantined=false WHERE organization=$1 AND id=$2`, scope.Organization, work.VersionID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE records SET withdrawn=true WHERE organization=$1 AND id=$2`, scope.Organization, work.RecordID); err != nil {
		t.Fatal(err)
	}
	assert(t, "unavailable")
	if _, err = pool.Exec(ctx, `UPDATE records SET withdrawn=false WHERE organization=$1 AND id=$2`, scope.Organization, work.RecordID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO tombstones VALUES($1,$2)`, scope.Organization, work.RecordID); err != nil {
		t.Fatal(err)
	}
	assert(t, "unavailable")
	// Withdrawing the shared target must not cascade to the source Manifest: the
	// resolver returns a fresh unavailable view each read.
	if _, err = pool.Exec(ctx, `DELETE FROM tombstones WHERE organization=$1 AND record_id=$2`, scope.Organization, work.RecordID); err != nil {
		t.Fatal(err)
	}
	assert(t, "available")
	denied := scope
	denied.Corpora = []string{"ungranted"}
	resolved, err := store.Resolve(ctx, denied, []content.Relation{relation})
	if err != nil {
		t.Fatal(err)
	}
	if resolved[0].Status != "unavailable" || resolved[0].TargetRecordID != "" {
		t.Fatalf("unauthorized Corpus scope resolved: %+v", resolved[0])
	}
	missing := relation
	missing.Target.RecordKey = "missing"
	resolved, err = store.Resolve(ctx, scope, []content.Relation{missing})
	if err != nil {
		t.Fatal(err)
	}
	if resolved[0].Status != "unavailable" || resolved[0].TargetRecordID != "" {
		t.Fatalf("missing target resolved: %+v", resolved[0])
	}
	foreign := scope
	foreign.Organization = "adapter-relations-other"
	resolved, err = store.Resolve(ctx, foreign, []content.Relation{relation})
	if err != nil {
		t.Fatal(err)
	}
	if resolved[0].Status != "unavailable" {
		t.Fatalf("cross-Organization target resolved: %+v", resolved[0])
	}
}
