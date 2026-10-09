package postgres_test

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/app"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Owns the per-file coverage and first-page catalog access paths. Existing
// bitmap and catalog traversal tests own their lifecycle semantics. Capture
// the real adapter queries so a copied, correct query cannot hide a regression.
func TestCoverageAndCatalogUseBoundedIndexLookups(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	pool := scratchDatabase(t, ctx)
	prior := embedded(t, regexp.MustCompile(`.*`))
	delete(prior, "20261009T0815Z_control_table_statistics.sql")
	if err := postgres.MigrateFS(ctx, pool, prior); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO corpora(organization,id,request_key,canonical_request,name,retrieval)
VALUES('example','existing','existing','','Existing catalog','{}')`); err != nil {
		t.Fatal(err)
	}
	if err := app.BootstrapDatabase(ctx, pool, app.Config{}.DeploymentSpaces(nil)); err != nil {
		t.Fatal(err)
	}
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	t.Run("control-table statistics", func(t *testing.T) {
		for _, table := range []string{"corpora", "tombstones", "corpus_projection_routes", "projection_generations", "storage_organizations", "storage_spaces"} {
			var analyzed, firstChange bool
			if err := pool.QueryRow(ctx, `SELECT reltuples>=0,
coalesce('autovacuum_analyze_threshold=0'=ANY(reloptions),false)
AND coalesce('autovacuum_analyze_scale_factor=0'=ANY(reloptions),false)
FROM pg_class WHERE oid=to_regclass($1)`, table).Scan(&analyzed, &firstChange); err != nil || !analyzed || !firstChange {
				t.Errorf("%s: analyzed=%v eligible after first change=%v err=%v", table, analyzed, firstChange, err)
			}
		}
	})
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	trace := &catalogAccessTrace{}
	cfg := pool.Config().Copy()
	cfg.ConnConfig.Tracer = trace
	traced, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer traced.Close()
	store := contentStores(traced)
	scope := corpus.Scope{Organization: "example", Actions: []string{"corpora:write", "content:write"}, Corpora: []string{"*"}}
	c, _, err := (postgres.Store{Pool: traced}).Create(ctx, scope.Organization, corpus.CreateInput{Key: "catalog", Name: "Catalog"})
	if err != nil {
		t.Fatal(err)
	}
	seg := rebuildSegmentation(t, ctx, store, scope, c.ID, "covered")
	g, err := store.Generation(ctx, scope.Organization, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Promote(ctx, scope.Organization, seg, g); err != nil {
		t.Fatal(err)
	}
	artifact := content.Embedding{Organization: scope.Organization, SegmentID: seg.Segments[0].ID, SpaceID: g.SpaceID}
	space := content.VectorSpace{ID: g.SpaceID}
	if err := pool.QueryRow(ctx, `SELECT manifest FROM vector_spaces WHERE id=$1`, space.ID).Scan(&space.Manifest); err != nil {
		t.Fatal(err)
	}
	if err := saveFixtureEmbedding(ctx, store, &artifact, space); err != nil {
		t.Fatal(err)
	}
	// Unrelated organizations and files make a missing leading key expensive.
	exec(`INSERT INTO storage_organizations(organization) SELECT 'other-'||i FROM generate_series(1,32) i;
INSERT INTO compact_embedding_coverage(organization_id,file_id,generation_id,covered)
SELECT o.id,1000+i,'other-generation',decode('01','hex')
FROM storage_organizations o CROSS JOIN generate_series(1,1000) i;
ANALYZE compact_embedding_coverage`)
	if err := store.CommitEnrichment(ctx, scope.Organization, seg, g, []content.Embedding{artifact}); err != nil {
		t.Fatal(err)
	}
	if trace.coverage.SQL == "" {
		t.Fatal("enrichment did not perform the coverage lookup")
	}
	t.Run("coverage", func(t *testing.T) {
		plan := explainCatalogAccess(t, ctx, pool, trace.coverage)
		found := false
		walkCatalogPlan(plan, func(node catalogAccessPlan) {
			if node.Relation != "compact_embedding_coverage" {
				return
			}
			found = true
			if node.NodeType != "Index Scan" || node.Index != "compact_embedding_coverage_pkey" ||
				!strings.Contains(node.Condition, "organization_id") || !strings.Contains(node.Condition, "file_id") || !strings.Contains(node.Condition, "generation_id") {
				t.Errorf("coverage must seek its full primary key, got %+v", node)
			}
		})
		if !found {
			t.Fatal("coverage relation missing from plan")
		}
	})
	// Seed a large catalog, but leave corpora without statistics as on a fresh
	// installation. Only the records' distribution is known to the planner.
	exec(`ALTER TABLE corpora SET (autovacuum_enabled=false)`)
	exec(`INSERT INTO records(organization,id,corpus_id,namespace,record_key)
SELECT 'example','bulk-'||lpad(i::text,5,'0'),$1,'example',i::text FROM generate_series(1,10000) i`, c.ID)
	exec(`UPDATE records SET current_accepted_at='2100-01-01'::timestamptz+substring(id FROM 6)::int*interval '1 second' WHERE id LIKE 'bulk-%';
ANALYZE records;
DELETE FROM pg_statistic WHERE starelid='corpora'::regclass;
UPDATE pg_class SET reltuples=-1,relpages=0 WHERE oid='corpora'::regclass`)
	for _, statistics := range []string{"missing", "analyzed"} {
		t.Run("catalog/"+statistics, func(t *testing.T) {
			if statistics == "analyzed" {
				exec("ANALYZE corpora")
			}
			page, err := store.Records(ctx, scope.Organization, c.ID, content.RecordQuery{Order: content.AcceptedAtDesc, Limit: 25})
			if err != nil || len(page) != 25 || page[0].ID != "bulk-10000" {
				t.Fatalf("newest page: rows=%d err=%v, want 25 starting at bulk-10000", len(page), err)
			}
			plan := explainCatalogAccess(t, ctx, pool, trace.catalog)
			found := false
			walkCatalogPlan(plan, func(node catalogAccessPlan) {
				if node.NodeType == "Sort" || node.NodeType == "Incremental Sort" {
					t.Errorf("first page must read in index order, got %+v", node)
				}
				if node.Relation == "records" {
					found = true
					if node.NodeType != "Index Scan" || node.Index != "records_catalog_accepted_order" {
						t.Errorf("catalog must use ordered index, got %+v", node)
					}
				}
			})
			if !found {
				t.Fatal("records relation missing from plan")
			}
		})
	}
	t.Run("archived corpora", func(t *testing.T) {
		other, _, err := (postgres.Store{Pool: pool}).Create(ctx, scope.Organization, corpus.CreateInput{Key: "other", Name: "Other catalog"})
		if err != nil {
			t.Fatal(err)
		}
		exec(`INSERT INTO records(organization,id,corpus_id,namespace,record_key) VALUES('example','other-record',$1,'example','other')`, other.ID)
		exec(`UPDATE corpora SET archived=true WHERE organization='example' AND id=$1`, c.ID)
		q := content.RecordQuery{Order: content.AcceptedAtDesc, Limit: 25}
		if page, err := store.Records(ctx, scope.Organization, c.ID, q); err != nil || len(page) != 0 {
			t.Fatalf("archived single corpus returned %d rows: %v", len(page), err)
		}
		q.CorpusIDs = []string{c.ID, other.ID}
		page, err := store.Records(ctx, scope.Organization, "", q)
		if err != nil || len(page) != 1 || page[0].ID != "other-record" {
			t.Fatalf("mixed corpora must return only the active record: %+v, err=%v", page, err)
		}
		if count, err := store.CountRecords(ctx, scope.Organization, "", q); err != nil || count != 1 {
			t.Fatalf("mixed corpora count=%d err=%v, want 1", count, err)
		}
	})
}

// Queries execute synchronously in this fixture. The tracer is attached only
// to the calling pool; EXPLAIN and fixture statements use the untraced pool.
type catalogAccessTrace struct {
	coverage, catalog pgx.TraceQueryStartData
}

func (r *catalogAccessTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	if strings.HasPrefix(d.SQL, "SELECT covered FROM compact_embedding_coverage") {
		r.coverage = d
	}
	if strings.HasPrefix(d.SQL, "SELECT id,corpus_id,namespace,record_key,withdrawn") {
		r.catalog = d
	}
	return ctx
}

func (*catalogAccessTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

type catalogAccessPlan struct {
	NodeType  string              `json:"Node Type"`
	Relation  string              `json:"Relation Name"`
	Index     string              `json:"Index Name"`
	Condition string              `json:"Index Cond"`
	Plans     []catalogAccessPlan `json:"Plans"`
}

func explainCatalogAccess(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query pgx.TraceQueryStartData) catalogAccessPlan {
	t.Helper()
	var raw []byte
	if err := pool.QueryRow(ctx, "EXPLAIN (FORMAT JSON) "+query.SQL, query.Args...).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var plans []struct{ Plan catalogAccessPlan }
	if err := json.Unmarshal(raw, &plans); err != nil || len(plans) != 1 {
		t.Fatalf("decode EXPLAIN: %s, err=%v", raw, err)
	}
	t.Logf("EXPLAIN: %s", raw)
	return plans[0].Plan
}

func walkCatalogPlan(plan catalogAccessPlan, visit func(catalogAccessPlan)) {
	visit(plan)
	for _, child := range plan.Plans {
		walkCatalogPlan(child, visit)
	}
}
