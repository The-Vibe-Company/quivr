package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
	"github.com/The-Vibe-Company/quivr/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Owns selective-page access paths; tiny pagination fixtures cannot catch a
// rare match forcing an ordered walk of the entire corpus. No latency assertion.
func TestMetadataCatalogIndexedPageWork(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := queueWorkPool(t, ctx)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	seedQueueCorpus(t, ctx, pool, "example")
	exec := func(sql string) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO records(organization,id,corpus_id,namespace,record_key,current_version_id)
 SELECT 'example','r'||lpad(i::text,6,'0'),'corpus','example',i::text,'v'||i FROM generate_series(1,50000) i`)
	exec(`INSERT INTO accepted_revisions(organization,record_id,slot,digest,version_id,acceptance_order,source_position,command)
 SELECT organization,id,'text',record_key,current_version_id,1,'','{}' FROM records`)
	exec(`UPDATE records SET current_accepted_at='2026-10-01'::timestamptz+record_key::int*interval '1 second'`)
	exec(`INSERT INTO projection_metadata(organization,version_id,generation_id,data)
 SELECT organization,current_version_id,'generation',jsonb_build_object('rare',record_key::int%2000=0,
 'one',record_key::int%100=0,'ten',record_key::int%10=0,'half',record_key::int%2=0,'same',record_key::int%2=0,'repeated',record_key::int%200=0,
 'tags',CASE WHEN record_key::int%2000=0 THEN '["rare","rare"]'::jsonb ELSE '["common"]'::jsonb END,
 'date',CASE WHEN record_key::int%2000=0 THEN '2026-10-02T00:00:00.000Z' ELSE '2026-10-01T00:00:00.000Z' END) FROM records`)
	// Bulk-load the query fixture before synchronization is installed. The
	// bootstrap owner below exercises real publication and replacement; replaying
	// that row-wise write path 50,000 times does not strengthen these plan checks.
	exec(`INSERT INTO projection_metadata_filter_values
 (organization,corpus_id,generation_id,version_id,field,value,date_epoch_ms)
 SELECT DISTINCT pm.organization,'corpus',pm.generation_id,pm.version_id,entry.key,member.value,
 CASE WHEN entry.key='date' THEN CASE WHEN member.value='"2026-10-02T00:00:00.000Z"'::jsonb
 THEN 1790899200000::bigint ELSE 1790812800000::bigint END END
 FROM projection_metadata pm CROSS JOIN LATERAL jsonb_each(pm.data) entry
 CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(entry.value)='array'
 THEN entry.value ELSE jsonb_build_array(entry.value) END) member`)
	exec(`UPDATE projection_metadata SET filter_indexed=true`)
	if err := EnsureIndexes(ctx, pool); err != nil {
		t.Fatal(err)
	}
	exec("ANALYZE")
	trace := &metadataPageTrace{}
	cfg := pool.Config()
	cfg.ConnConfig.Tracer = trace
	observed, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer observed.Close()
	store := RecordStore{Pool: observed}
	for _, order := range []content.RecordOrder{content.RecordIDOrder, content.AcceptedAtDesc} {
		for _, field := range []string{"rare", "date", "tags", "one", "ten", "half", "common-first", "repeated"} {
			t.Run(fmt.Sprintf("%s/%s", order, field), func(t *testing.T) {
				f := corpus.TypedFilter{MetadataFilter: corpus.MetadataFilter{Field: field, AnyOf: []any{true}}, Type: "boolean"}
				want, limit := 2000, 5
				if order == content.AcceptedAtDesc {
					want = 50000
				}
				if field == "date" {
					f = corpus.TypedFilter{MetadataFilter: corpus.MetadataFilter{Field: "date", Gte: "2026-10-02T00:00:00.000Z", Lte: "2026-10-02T00:00:00.000Z"}, Type: "datetime"}
				}
				if field == "tags" {
					f = corpus.TypedFilter{MetadataFilter: corpus.MetadataFilter{Field: "tags", AnyOf: []any{"rare", "rare"}}, Type: "string_array"}
				}
				if field == "repeated" {
					f.AnyOf = make([]any, 50)
					for i := range f.AnyOf {
						f.AnyOf[i] = true
					}
					limit = 101
					if order == content.RecordIDOrder {
						want = 200
					}
				}
				if field == "one" || field == "ten" || field == "half" {
					limit = 101
					if order == content.RecordIDOrder {
						want = map[string]int{"one": 100, "ten": 10, "half": 2}[field]
					}
				}
				filters := []corpus.TypedFilter{f}
				if field == "common-first" {
					filters = []corpus.TypedFilter{{MetadataFilter: corpus.MetadataFilter{Field: "half", AnyOf: []any{true}}, Type: "boolean"}, {MetadataFilter: corpus.MetadataFilter{Field: "rare", AnyOf: []any{true}}, Type: "boolean"}}
				}
				q := content.RecordQuery{CorpusIDs: []string{"corpus"}, Metadata: []corpus.MetadataFilter{{Field: "requested"}}, FilterRoutes: []content.CatalogFilterRoute{{CorpusID: "corpus", GenerationID: "generation", Filters: filters}}, Order: order, Limit: limit}
				for _, deep := range []bool{false, true} {
					trace.queries = nil
					got, err := store.Records(ctx, "example", "corpus", q)
					if err != nil || len(got) != limit {
						t.Fatalf("deep=%t got %d records: %v", deep, len(got), err)
					}
					step := 2000
					if field == "repeated" {
						step = 200
					}
					if field == "one" || field == "ten" || field == "half" {
						step = map[string]int{"one": 100, "ten": 10, "half": 2}[field]
					}
					if order == content.AcceptedAtDesc {
						step = -step
					}
					start := want
					if deep {
						start += step * limit
					}
					for i, row := range got {
						expected := fmt.Sprintf("r%06d", start+step*i)
						if row.ID != expected {
							t.Fatalf("deep=%t item %d: %s, want %s", deep, i, row.ID, expected)
						}
					}
					if deep && got[0].ID == q.AfterID {
						t.Fatal("cursor repeated its record")
					}
					indexed := false
					for _, query := range trace.queries {
						if !strings.HasPrefix(query.sql, "WITH ") && !strings.HasPrefix(query.sql, "SELECT ") {
							continue
						}
						var raw []byte
						if err := pool.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+query.sql, query.args...).Scan(&raw); err != nil {
							t.Fatal(err)
						}
						var plans []struct{ Plan map[string]any }
						if err := json.Unmarshal(raw, &plans); err != nil {
							t.Fatal(err)
						}
						var visit func(map[string]any)
						visit = func(n map[string]any) {
							if n["Index Name"] == "projection_metadata_filter_values_lookup" || n["Index Name"] == "projection_metadata_filter_dates_lookup" {
								indexed = true
								if n["Actual Rows"].(float64) > 10001 {
									t.Fatalf("unbounded metadata anchor: %s", raw)
								}
							}
							if n["Relation Name"] == "projection_metadata_filter_values" {
								kind, _ := n["Node Type"].(string)
								if kind == "Seq Scan" {
									t.Fatalf("metadata index missing: %s", raw)
								}
								if strings.Contains(kind, "Index") {
									indexed = true
								}
							}
							if n["Relation Name"] == "records" {
								work := n["Actual Rows"].(float64)
								if removed, ok := n["Rows Removed by Filter"].(float64); ok {
									work += removed
								}
								if work > 16384 {
									t.Fatalf("unbounded record walk: %s", raw)
								}
							}
							children, _ := n["Plans"].([]any)
							for _, child := range children {
								visit(child.(map[string]any))
							}
						}
						visit(plans[0].Plan)
					}
					if (field == "rare" || field == "date" || field == "tags" || field == "common-first") && !indexed {
						t.Fatal("selective metadata page never used its scoped value/date index")
					}
					last := got[len(got)-1]
					q.AfterID = last.ID
					q.AfterAcceptedAt = last.CurrentAcceptedAt
				}
			})
		}
	}
	t.Run("date-equality-with-broad-range", func(t *testing.T) {
		f := corpus.TypedFilter{MetadataFilter: corpus.MetadataFilter{Field: "date", AnyOf: []any{"2026-10-02T00:00:00.000Z"}, Gte: "2026-10-01T00:00:00.000Z"}, Type: "datetime"}
		got, err := store.Records(ctx, "example", "corpus", content.RecordQuery{Metadata: []corpus.MetadataFilter{f.MetadataFilter}, FilterRoutes: []content.CatalogFilterRoute{{CorpusID: "corpus", GenerationID: "generation", Filters: []corpus.TypedFilter{f}}}, Limit: 101})
		if err != nil || len(got) != 25 {
			t.Fatalf("selective date equality lost behind its broad range: %v, %v", got, err)
		}
		for i, row := range got {
			if want := fmt.Sprintf("r%06d", (i+1)*2000); row.ID != want {
				t.Fatalf("date intersection item %d: %s, want %s", i, row.ID, want)
			}
		}
	})
	t.Run("incomplete-intersection", func(t *testing.T) {
		filters := []corpus.TypedFilter{{MetadataFilter: corpus.MetadataFilter{Field: "half", AnyOf: []any{true}}, Type: "boolean"}, {MetadataFilter: corpus.MetadataFilter{Field: "same", AnyOf: []any{false}}, Type: "boolean"}}
		got, err := store.Records(ctx, "example", "corpus", content.RecordQuery{CorpusIDs: []string{"corpus"}, Metadata: []corpus.MetadataFilter{{Field: "requested"}}, FilterRoutes: []content.CatalogFilterRoute{{CorpusID: "corpus", GenerationID: "generation", Filters: filters}}, Limit: 5})
		if !errors.Is(err, publicerr.FilterTooBroad) || got != nil {
			t.Fatalf("incomplete intersection returned a misleading page: %v, %v", got, err)
		}
		canceled, stop := context.WithCancel(ctx)
		stop()
		_, err = store.Records(canceled, "example", "corpus", content.RecordQuery{Metadata: []corpus.MetadataFilter{{Field: "requested"}}, Limit: 5})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("caller cancellation became capacity refusal: %v", err)
		}
	})
}

// Owns historical bootstrap, interrupted source locking, replacement and delete.
// A real old-schema fixture tests rollout; the page owner tests read work.
func TestMetadataCatalogIndexBootstrap(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := queueWorkPool(t, ctx)
	old := fstest.MapFS{}
	names, err := fs.ReadDir(migrations.Files, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name.Name() == "20261010T1200Z_metadata_catalog_index.sql" {
			continue
		}
		raw, err := fs.ReadFile(migrations.Files, name.Name())
		if err != nil {
			t.Fatal(err)
		}
		old[name.Name()] = &fstest.MapFile{Data: raw}
	}
	if err := MigrateFS(ctx, pool, old); err != nil {
		t.Fatal(err)
	}
	seedQueueCorpus(t, ctx, pool, "example")
	exec := func(sql string) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO records(organization,id,corpus_id,namespace,record_key,current_version_id)
 SELECT 'example','r'||lpad(i::text,6,'0'),'corpus','example',i::text,'v'||i FROM generate_series(1,600) i`)
	exec(`INSERT INTO accepted_revisions(organization,record_id,slot,digest,version_id,acceptance_order,source_position,command)
 SELECT organization,id,'text',record_key,current_version_id,1,'','{}' FROM records`)
	exec(`INSERT INTO projection_metadata(organization,version_id,generation_id,data)
 SELECT organization,current_version_id,'generation',jsonb_build_object('kind',CASE WHEN record_key='600' THEN 'rare' ELSE 'common' END,'tags','["old","old"]'::jsonb,'rank',2,'urgent',true,'date','2026-10-01T00:00:00.000Z') FROM records`)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store := RecordStore{Pool: pool}
	read := func(field, typ string, value any, want string) {
		t.Helper()
		f := corpus.TypedFilter{MetadataFilter: corpus.MetadataFilter{Field: field, AnyOf: []any{value}}, Type: typ}
		q := content.RecordQuery{Metadata: []corpus.MetadataFilter{f.MetadataFilter}, FilterRoutes: []content.CatalogFilterRoute{{CorpusID: "corpus", GenerationID: "generation", Filters: []corpus.TypedFilter{f}}}, Limit: 1}
		got, err := store.Records(ctx, "example", "corpus", q)
		if err != nil || len(got) != 1 || got[0].ID != want {
			t.Fatalf("bootstrap page %s: %v, %v; want %s", field, got, err, want)
		}
	}
	read("kind", "string", "rare", "r000600") // no derived entries yet; no silent omission
	dates := content.RecordQuery{Metadata: []corpus.MetadataFilter{{Field: "date"}}, FilterRoutes: []content.CatalogFilterRoute{{CorpusID: "corpus", GenerationID: "generation", Filters: []corpus.TypedFilter{{MetadataFilter: corpus.MetadataFilter{Field: "date", Gte: "2026-10-01T00:00:00.000Z"}, Type: "datetime"}}}}, Limit: 1}
	if got, err := store.Records(ctx, "example", "corpus", dates); !errors.Is(err, publicerr.ContentUnavailable) || len(got) != 0 {
		t.Fatalf("uninstalled date helper must stay transient: %v, %v", got, err)
	}
	if err := EnsureIndexes(ctx, pool); err != nil {
		t.Fatal(err)
	}
	read("kind", "string", "rare", "r000600")
	// Simulate a process stopping between committed bootstrap batches. A locked
	// source is skipped while another pending source makes progress.
	exec(`DELETE FROM projection_metadata_filter_values WHERE version_id IN ('v599','v600'); UPDATE projection_metadata SET filter_indexed=false WHERE version_id IN ('v599','v600')`)
	writer, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback(ctx)
	if _, err = writer.Exec(ctx, `SELECT FROM projection_metadata WHERE version_id='v600' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	if err := EnsureIndexes(ctx, pool); !errors.Is(err, ErrIndexBusy) {
		t.Fatalf("locked bootstrap source was lost: %v", err)
	}
	read("kind", "string", "rare", "r000600")
	if _, err = writer.Exec(ctx, `UPDATE projection_metadata SET data='{"kind":"replacement","tags":["updated","updated"],"rank":2,"urgent":true,"date":"-0001-12-31T23:00:00.000Z"}' WHERE version_id='v600'`); err != nil {
		t.Fatal(err)
	}
	if err := writer.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	exec(`DELETE FROM projection_metadata WHERE version_id='v599'`)
	if err := EnsureIndexes(ctx, pool); err != nil {
		t.Fatal(err)
	}
	read("kind", "string", "replacement", "r000600")
	read("tags", "string_array", "updated", "r000600")
	var pending, obsolete int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM projection_metadata WHERE NOT filter_indexed`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM projection_metadata_filter_values WHERE version_id='v599' OR (version_id='v600' AND value='"old"'::jsonb)`).Scan(&obsolete); err != nil {
		t.Fatal(err)
	}
	if pending != 0 || obsolete != 0 {
		t.Fatalf("bootstrap revived obsolete entries: pending=%d obsolete=%d", pending, obsolete)
	}
	for _, tc := range []struct{ source, bound string }{
		{"-0001-12-31T23:00:00.000Z", "0000-01-01T00:00:00+01:00"},
		{"0000-01-01T00:00:00.000Z", "0000-01-01T00:00:00Z"},
		{"10000-01-01T01:00:00.000Z", "9999-12-31T23:00:00-02:00"},
	} {
		if _, err := pool.Exec(ctx, `UPDATE projection_metadata SET data=jsonb_set(data,'{date}',to_jsonb($1::text)) WHERE version_id='v600'`, tc.source); err != nil {
			t.Fatal(err)
		}
		filters := []corpus.MetadataFilter{{Field: "date", Gte: tc.bound, Lte: tc.bound}}
		typed, _, err := corpus.ResolveFilters(filters, []corpus.Field{{Name: "date", Type: "datetime", Roles: []string{"filter"}}})
		if err != nil {
			t.Fatal(err)
		}
		got, err := store.Records(ctx, "example", "corpus", content.RecordQuery{AfterID: "r000512", Metadata: filters, FilterRoutes: []content.CatalogFilterRoute{{CorpusID: "corpus", GenerationID: "generation", Filters: typed}}, Limit: 1})
		if err != nil || len(got) != 1 || got[0].ID != "r000600" {
			t.Fatalf("UTC boundary %s: %v, %v", tc.bound, got, err)
		}
	}

}

type metadataPageQuery struct {
	sql  string
	args []any
}
type metadataPageTrace struct{ queries []metadataPageQuery }

func (s *metadataPageTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	s.queries = append(s.queries, metadataPageQuery{d.SQL, append([]any(nil), d.Args...)})
	return ctx
}
func (*metadataPageTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
