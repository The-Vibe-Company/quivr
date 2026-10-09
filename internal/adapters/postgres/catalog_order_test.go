package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This owns ordered-page work, including the singleton CorpusIDs used by the
// content service. Traversal correctness remains with the catalog owners.
// Real plans catch a corpus scan/sort that tiny correctness fixtures cannot.
func TestCatalogOrderedPageWork(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := queueWorkPool(t, ctx)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := EnsureIndexes(ctx, pool); err != nil {
		t.Fatal(err)
	}
	seedQueueCorpus(t, ctx, pool, "example")
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO records(organization,id,corpus_id,namespace,record_key,current_accepted_at)
 SELECT 'example','r'||lpad(i::text,6,'0'),'corpus','example',i::text,'2026-10-01'::timestamptz+i*interval '1 second' FROM generate_series(1,100000) i`)
	exec(`UPDATE records SET current_accepted_at='2026-10-01'::timestamptz+record_key::int*interval '1 second'`)
	exec("ANALYZE")
	trace := &orderedPageTrace{}
	cfg := pool.Config()
	cfg.ConnConfig.Tracer = trace
	observed, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer observed.Close()
	store := RecordStore{Pool: observed}
	for _, order := range []content.RecordOrder{content.RecordIDOrder, content.AcceptedAtDesc} {
		for _, deep := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/deep=%t", order, deep), func(t *testing.T) {
				q := content.RecordQuery{CorpusIDs: []string{"corpus"}, Order: order, Limit: 25}
				want := "r000001"
				if order == content.AcceptedAtDesc {
					want = "r100000"
				}
				if deep {
					if order == content.AcceptedAtDesc {
						at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).Add(101 * time.Second)
						q.AfterID, q.AfterAcceptedAt, want = "r000101", &at, "r000100"
					} else {
						q.AfterID, want = "r099900", "r099901"
					}
				}
				rows, err := store.Records(ctx, "example", "corpus", q)
				if err != nil || len(rows) != 25 || rows[0].ID != want {
					t.Fatalf("page: %+v, want 25 starting %s: %v", rows, want, err)
				}
				raw := trace.explain(t, ctx, pool)
				checkOrderedPagePlan(t, raw, 1, 25)
			})
		}
	}
	t.Run("duplicate-corpus", func(t *testing.T) {
		rows, err := store.Records(ctx, "example", "corpus", content.RecordQuery{CorpusIDs: []string{"corpus", "corpus"}, Limit: 2})
		if err != nil || len(rows) != 2 || rows[0].ID != "r000001" || rows[1].ID != "r000002" {
			t.Fatalf("duplicate corpus repeated catalog records: %+v, %v", rows, err)
		}
		checkOrderedPagePlan(t, trace.explain(t, ctx, pool), 1, 2)
	})

	exec(`INSERT INTO corpora(organization,id,request_key,canonical_request,name,retrieval) VALUES('example','other','other','','Other','{}')`)
	exec(`INSERT INTO records(organization,id,corpus_id,namespace,record_key)
 SELECT 'example','t'||lpad(i::text,6,'0'),'other','example',i::text FROM generate_series(1,100) i`)
	exec(`UPDATE records SET current_accepted_at='2026-10-01'::timestamptz+record_key::int*interval '1 second' WHERE corpus_id='other'`)
	exec("ANALYZE")
	for _, order := range []content.RecordOrder{content.RecordIDOrder, content.AcceptedAtDesc} {
		for _, deep := range []bool{false, true} {
			t.Run(fmt.Sprintf("multi/%s/deep=%t", order, deep), func(t *testing.T) {
				q := content.RecordQuery{CorpusIDs: []string{"corpus", "other"}, Order: order, Limit: 25}
				want := "r000001"
				if order == content.AcceptedAtDesc {
					want = "r100000"
				}
				if deep {
					if order == content.AcceptedAtDesc {
						at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).Add(101 * time.Second)
						q.AfterID, q.AfterAcceptedAt, want = "r000101", &at, "t000100"
					} else {
						q.AfterID, want = "r099900", "r099901"
					}
				}
				rows, err := store.Records(ctx, "example", "corpus", q)
				if err != nil || len(rows) != 25 || rows[0].ID != want {
					t.Fatalf("multi page: %+v, want 25 starting %s: %v", rows, want, err)
				}
				checkOrderedPagePlan(t, trace.explain(t, ctx, pool), 2, 25)
			})
		}
	}

}

// Preview owns a different work bound: current records, rather than receipt
// history, determine the read cost. The existing preview owner covers lifecycle.
func TestPreviewOrderedPageWork(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := queueWorkPool(t, ctx)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := EnsureIndexes(ctx, pool); err != nil {
		t.Fatal(err)
	}
	seedQueueCorpus(t, ctx, pool, "example")
	exec := func(sql string) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO corpora(organization,id,request_key,canonical_request,name,retrieval) VALUES('example','other','other','','Other','{}')`)
	exec(`INSERT INTO records(organization,id,corpus_id,namespace,record_key)
 SELECT 'example','r'||lpad(i::text,6,'0'),CASE WHEN i%2=0 THEN 'corpus' ELSE 'other' END,'example',i::text FROM generate_series(1,10000) i`)
	exec(`INSERT INTO record_versions(organization,id,record_id,slot,digest,acceptance_order,source_position,text_blob_id,manifest_blob_id,provenance,baseline_ready)
 SELECT organization,'v'||record_key,id,'text',record_key,1,'','blob','blob','{}',true FROM records`)
	exec(`INSERT INTO ingestion_receipts(organization,id,request_key,canonical_request,command,corpus_id,record_id,acceptance_order,slot,digest,accepted_at)
 SELECT organization,'receipt-'||record_key,record_key,'','{}',corpus_id,id,1,'text',record_key,'2026-10-01'::timestamptz+record_key::int*interval '1 second' FROM records`)
	exec("ANALYZE")
	exec(`UPDATE records SET current_version_id='v'||record_key`)
	// Historical receipts are newer than the current versions. They cannot
	// contribute a candidate and must not dominate page selection work.
	exec(`INSERT INTO ingestion_receipts(organization,id,request_key,canonical_request,command,corpus_id,record_id,acceptance_order,slot,digest,accepted_at)
 SELECT 'example','history-'||i,'history-'||i,'','{}','corpus','r010000',i+1,'text',i::text,'2026-10-02'::timestamptz+i*interval '1 second' FROM generate_series(1,20000) i`)
	exec("ANALYZE")
	trace := &orderedPageTrace{}
	cfg := pool.Config()
	cfg.ConnConfig.Tracer = trace
	observed, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer observed.Close()
	store := EvaluationStore{Pool: observed}
	for _, ids := range [][]string{{"corpus"}, {"corpus", "other"}} {
		for _, bounded := range []bool{false, true} {
			t.Run(fmt.Sprintf("corpora=%d/window=%t", len(ids), bounded), func(t *testing.T) {
				after := time.Time{}
				if bounded {
					after = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).Add(9000 * time.Second)
				}
				got, err := store.Recent(ctx, "example", ids, after, 25)
				if err != nil || len(got) != 25 || got[0].RecordID != "r010000" {
					t.Fatalf("preview: %+v, %v", got, err)
				}
				checkOrderedPagePlan(t, trace.explain(t, ctx, pool), len(ids), 25)
			})
		}
	}
	t.Run("duplicate-corpus", func(t *testing.T) {
		got, err := store.Recent(ctx, "example", []string{"corpus", "corpus"}, time.Time{}, 2)
		if err != nil || len(got) != 2 || got[0].RecordID != "r010000" || got[1].RecordID != "r009998" {
			t.Fatalf("duplicate corpus repeated preview versions: %+v, %v", got, err)
		}
		checkOrderedPagePlan(t, trace.explain(t, ctx, pool), 1, 2)
	})

	// Version IDs deliberately have a different lexicographic order from
	// Record IDs here (v9999 versus v10000). This catches the old tie-breaker.
	exec(`UPDATE ingestion_receipts SET accepted_at='2026-10-01'::timestamptz+interval '10000 seconds'
 WHERE acceptance_order=1 AND record_id IN ('r010000','r009999','r009998')`)
	exec(`UPDATE records SET current_version_id=current_version_id WHERE id IN ('r010000','r009999','r009998')`)
	t.Run("ties", func(t *testing.T) {
		got, err := store.Recent(ctx, "example", []string{"corpus", "other"}, time.Time{}, 2)
		if err != nil || len(got) != 2 || got[0].RecordID != "r010000" || got[0].VersionID != "v10000" || got[1].RecordID != "r009999" || got[1].VersionID != "v9999" {
			t.Fatalf("tied preview wants r010000@v10000 then r009999@v9999: %+v, %v", got, err)
		}
		checkOrderedPagePlan(t, trace.explain(t, ctx, pool), 2, 2)
	})

}

// Captures the actual adapter query through pgx's production tracing seam;
// explaining it does not require a second hand-maintained SQL implementation.
type orderedPageTrace struct {
	sql  string
	args []any
}

func (s *orderedPageTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	s.sql, s.args = d.SQL, append([]any(nil), d.Args...)
	return ctx
}
func (*orderedPageTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
func (s *orderedPageTrace) explain(t *testing.T, ctx context.Context, pool *pgxpool.Pool) []byte {
	t.Helper()
	if s.sql == "" {
		t.Fatal("adapter issued no query")
	}
	var raw []byte
	if err := pool.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+s.sql, s.args...).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}
func checkOrderedPagePlan(t *testing.T, raw []byte, corpora, limit int) {
	t.Helper()
	var plans []struct{ Plan map[string]any }
	if err := json.Unmarshal(raw, &plans); err != nil {
		t.Fatal(err)
	}
	scans := 0
	var visit func(map[string]any)
	visit = func(n map[string]any) {
		kind, _ := n["Node Type"].(string)
		if strings.Contains(kind, "Sort") && (corpora == 1 || n["Actual Rows"].(float64) > float64(corpora*limit)) {
			t.Fatalf("unbounded page sort: %s", raw)
		}
		if n["Relation Name"] == "segments" && n["Actual Loops"].(float64) > float64(limit) {
			t.Fatalf("enrichment ran before the global limit: %s", raw)
		}
		if n["Relation Name"] == "records" {
			if kind != "Index Scan" && kind != "Index Only Scan" {
				t.Fatalf("page did not use ordered records index: %s", raw)
			}
			if n["Actual Rows"].(float64) > float64(limit+1) {
				t.Fatalf("page scanned more records than its limit: %s", raw)
			}
			scans += int(n["Actual Loops"].(float64))
		}
		children, _ := n["Plans"].([]any)
		for _, child := range children {
			visit(child.(map[string]any))
		}
	}
	visit(plans[0].Plan)
	if scans != corpora {
		t.Fatalf("want %d ordered corpus scans, got %d: %s", corpora, scans, raw)
	}
	blocks := queueWorkBlocks(t, raw)
	if blocks > 1000 {
		t.Fatalf("page used %d buffers, want bounded page work: %s", blocks, raw)
	}
	t.Logf("ordered page: %d corpus scans, %d buffers", scans, blocks)
}
