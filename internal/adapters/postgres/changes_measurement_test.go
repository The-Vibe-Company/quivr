package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/app"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Opt-in local measurement, outside the PR gate. The coordinator runs this on
// an isolated machine with room for 80M journal rows and their indexes:
// QUIVR_MEASURE=1 make adapter-postgres args='-v -timeout 2h -run ^TestChangeReadCorpusIsolationMeasurement$'
func TestChangeReadCorpusIsolationMeasurement(t *testing.T) {
	if os.Getenv("QUIVR_MEASURE") != "1" {
		t.Skip("opt-in local 80M-row measurement: set QUIVR_MEASURE=1")
	}
	ctx := t.Context()
	pool := scratchDatabase(t, ctx)
	if err := app.BootstrapDatabase(ctx, pool, app.Config{}.DeploymentSpaces(nil)); err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO organization_journals(organization) VALUES('example');
INSERT INTO corpora(organization,id,request_key,canonical_request,name,retrieval)
VALUES('example','sparse','sparse','','Sparse','{}'),('example','quiet','quiet','','Quiet','{}')`)
	const rows = 80_000_000
	started := time.Now()
	for lower := 0; lower < rows; lower += 1_000_000 {
		exec(`INSERT INTO change_events(organization,sequence,event_id,corpus_id,event_type,resource_type,resource_id)
SELECT 'example',i,'changes-corpus-isolation-'||i,
CASE WHEN i%1000000=0 THEN 'sparse' ELSE 'busy' END,
'record.accepted','record','example' FROM generate_series($1::bigint+1,$2::bigint) i`, lower, lower+1_000_000)
		t.Logf("seeded %d/%d rows in %s", lower+1_000_000, rows, time.Since(started))
	}
	exec(`UPDATE organization_journals SET last_sequence=$1 WHERE organization='example'`, rows)
	started = time.Now()
	if err := postgres.EnsureIndexes(ctx, pool); err != nil {
		t.Fatal(err)
	}
	exec("ANALYZE change_events")
	var indexBytes int64
	if err := pool.QueryRow(ctx, `SELECT pg_relation_size('change_events_by_corpus')`).Scan(&indexBytes); err != nil {
		t.Fatal(err)
	}
	t.Logf("corpus index setup=%s bytes=%d", time.Since(started), indexBytes)
	trace := &changeReadTrace{}
	cfg := pool.Config().Copy()
	cfg.ConnConfig.Tracer = trace
	traced, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer traced.Close()
	store := postgres.ChangeStore{Pool: traced}
	for _, rate := range []int{0, 5_000, 20_000} {
		t.Run(fmt.Sprintf("neighbour-events-per-second-%d", rate), func(t *testing.T) {
			writerCtx, cancel := context.WithCancel(ctx)
			var written atomic.Int64
			done := make(chan error, 1)
			go func() {
				ticker := time.NewTicker(100 * time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case <-writerCtx.Done():
						done <- nil
						return
					case <-ticker.C:
						if rate == 0 {
							continue
						}
						_, err := pool.Exec(writerCtx, `WITH head AS (
UPDATE organization_journals SET last_sequence=last_sequence+$1 WHERE organization='example' RETURNING last_sequence)
INSERT INTO change_events(organization,sequence,event_id,corpus_id,event_type,resource_type,resource_id)
SELECT 'example',i,'changes-corpus-isolation-'||i,'busy','record.accepted','record','example'
FROM head CROSS JOIN LATERAL generate_series(last_sequence-$1+1,last_sequence) i`, rate/10)
						if err != nil {
							done <- err
							return
						}
						written.Add(int64(rate / 10))
					}
				}
			}()
			defer func() {
				cancel()
				if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
					t.Error(err)
				}
			}()
			began := time.Now()
			for _, corpusID := range []string{"quiet", "sparse"} {
				for _, after := range []int64{0, rows - 1000} {
					var latencies []time.Duration
					pacer := time.NewTicker(10 * time.Millisecond)
					for range 200 {
						<-pacer.C
						start := time.Now()
						w, err := store.ReadChanges(ctx, "example", corpusID, after, 100, 24*time.Hour)
						latencies = append(latencies, time.Since(start))
						want := 0
						if corpusID == "sparse" {
							want = 80
							if after > 0 {
								want = 1
							}
						}
						if err != nil || len(w.Events) != want || w.Through != w.Head || w.Head < rows || w.Expired {
							pacer.Stop()
							t.Fatalf("%s after %d: window=%+v err=%v want %d events at head", corpusID, after, w, err, want)
						}
						for _, e := range w.Events {
							if e.Position%1_000_000 != 0 || e.Position > rows {
								t.Fatalf("foreign event: %+v", e)
							}
						}
					}
					pacer.Stop()
					slices.Sort(latencies)
					t.Logf("%s after %d: p50=%s p95=%s max=%s", corpusID, after, latencies[99], latencies[189], latencies[199])
					assertChangeReadPlan(t, ctx, pool, trace.query, 101)
					if latencies[189] >= 100*time.Millisecond {
						t.Errorf("p95 %s exceeds 100ms", latencies[189])
					}
				}
			}
			t.Logf("neighbour achieved %.0f events/s", float64(written.Load())/time.Since(began).Seconds())
		})
	}
}
