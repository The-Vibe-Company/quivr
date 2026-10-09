// This storage measurement runs on an empty, task-owned PostgreSQL database.
// It measures routing lock wait on actual inline import journal batches. It
// does not measure external vector-index or provider throughput.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/app"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/plugins/registry"
	"github.com/The-Vibe-Company/quivr/internal/routing"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type lockSample struct {
	started time.Time
	sql     string
}
type traceKey struct{}
type waits struct {
	sync.Mutex
	Samples int
	Max     time.Duration
}

func (w *waits) sample(ctx context.Context, sql string) {
	if strings.Contains(sql, "pg_advisory_xact_lock_shared") {
		v := ctx.Value(traceKey{}).(lockSample)
		w.Lock()
		w.Samples++
		w.Max = max(w.Max, time.Since(v.started))
		w.Unlock()
	}
}
func (w *waits) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, traceKey{}, lockSample{time.Now(), d.SQL})
}
func (w *waits) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	w.sample(ctx, ctx.Value(traceKey{}).(lockSample).sql)
}
func (w *waits) TraceBatchStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceBatchStartData) context.Context {
	return context.WithValue(ctx, traceKey{}, lockSample{started: time.Now()})
}
func (w *waits) TraceBatchQuery(ctx context.Context, _ *pgx.Conn, d pgx.TraceBatchQueryData) {
	w.sample(ctx, d.SQL)
}
func (*waits) TraceBatchEnd(context.Context, *pgx.Conn, pgx.TraceBatchEndData) {}
func must(err error) {
	if err != nil {
		panic(err)
	}
}
func main() {
	dsn := flag.String("database", "", "empty PostgreSQL database named routing_scale...")
	documents := flag.Int("documents", 1_000_000, "documents to seed")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, *dsn)
	must(err)
	defer pool.Close()
	var name string
	must(pool.QueryRow(ctx, `SELECT current_database()`).Scan(&name))
	if !strings.HasPrefix(name, "routing_scale") {
		panic("use a task-owned routing_scale database")
	}
	must(postgres.Migrate(ctx, pool))
	pins, err := plugins.LoadPins([]plugins.PinConfig{
		{Manifest: "sdks/go/examples/hash-embedder/quivr-plugin.yaml", Endpoint: "http://127.0.0.1:1", Spaces: map[string]string{"example.hash_embedder.small": "served", "example.hash_embedder.large": "evaluation"}},
		{Manifest: "tests/plugin-contract/ingestion-valid/quivr-plugin.yaml", Endpoint: "http://127.0.0.1:1", Spaces: map[string]string{"certified.ingestion-valid.small": "served", "certified.ingestion-valid.large": "evaluation"}},
	})
	must(err)
	must(pins.ConfigureIngestion(plugins.IngestionRouting{Default: "example.hash_embedder", Evaluation: map[string][]string{"text/plain": {"certified.ingestion-valid"}}}))
	must(app.BootstrapDatabase(ctx, pool, app.Config{}.DeploymentSpaces(pins)))
	pluginStore := postgres.PluginStore{Pool: pool}
	seeded, err := pluginStore.ApplyConfiguration(ctx, registry.FromPins(pins))
	must(err)
	var target string
	for _, r := range registry.FromPins(pins).Registrations {
		if r.PluginID == "certified.ingestion-valid" {
			target = r.ID
		}
	}
	// A deterministic discovery stand-in isolates SQL preparation and lock wait;
	// real returning-provider discovery is covered by the adapter lifecycle owner.
	store := postgres.RoutingStore{Pool: pool, Registry: registry.Service{Store: pluginStore, Spaces: app.Config{}.DeploymentSpaces, Reach: func(context.Context, registry.Registration) error { return nil }}}
	started := time.Now()
	seed(ctx, pool, *documents)
	fmt.Printf("seed documents=%d elapsed=%s\n", *documents, time.Since(started))
	for _, q := range []string{
		`EXPLAIN (ANALYZE,BUFFERS) SELECT organization,id FROM records WHERE (organization,id)>('scale','r0000500000') ORDER BY organization,id LIMIT 256`,
		`EXPLAIN (ANALYZE,BUFFERS) SELECT count(*) FROM segments WHERE organization='scale' AND version_id='v0000500000' AND segmentation_id='sa0000500000'`,
	} {
		rows, err := pool.Query(ctx, q)
		must(err)
		for rows.Next() {
			var line string
			must(rows.Scan(&line))
			fmt.Println(line)
		}
		must(rows.Err())
		rows.Close()
	}
	commands := []routing.Command{{Kind: routing.KindPromotion, Target: "example.hash_embedder.large@1", Key: "promote"}, {Kind: routing.KindActivation, Target: target, Key: "activate"}, {Kind: routing.KindRollback, Target: seeded.Plan, Key: "rollback", PinnedWork: registry.PinnedWorkStop}}
	for _, command := range commands {
		if command.Kind == routing.KindRollback {
			_, err = pool.Exec(ctx, `UPDATE compact_embedding_coverage c SET covered='\x00' FROM embedding_files f WHERE f.id=c.file_id AND f.space_id=(SELECT id FROM storage_spaces WHERE space_id='example.hash_embedder.large@1') AND f.version_id>'v'||lpad(($1-1000)::text,10,'0')`, *documents)
			must(err)
		}
		measure(ctx, pool, *dsn, store, command)
	}
}
func measure(ctx context.Context, pool *pgxpool.Pool, dsn string, store postgres.RoutingStore, c routing.Command) {
	stats := &waits{}
	cfg, err := pgxpool.ParseConfig(dsn)
	must(err)
	cfg.ConnConfig.Tracer = stats
	writers, err := pgxpool.NewWithConfig(ctx, cfg)
	must(err)
	defer writers.Close()
	imports := content.Service{Submissions: postgres.SubmissionStore{Pool: writers}}
	scope := corpus.Scope{Organization: "scale", Actions: []string{"content:write"}, Corpora: []string{"*"}}
	writerCtx, stop := context.WithCancel(ctx)
	writerDone := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for i := 0; ; i++ {
			select {
			case <-writerCtx.Done():
				writerDone <- nil
				return
			case <-ticker.C:
			}
			_, err := imports.Accept(writerCtx, scope, content.Command{Key: fmt.Sprintf("%s-%d", c.Kind, i), Source: content.Source{CorpusID: "c0", Namespace: "import", RecordKey: fmt.Sprintf("%s-%d", c.Kind, i)}, Content: content.Text{Kind: "text", Text: "A neutral document arriving during routing preparation."}})
			if err != nil {
				if writerCtx.Err() != nil {
					writerDone <- nil
				} else {
					writerDone <- err
				}
				return
			}
		}
	}()
	begin := time.Now()
	op, err := store.AcceptRouting(ctx, "scale", c)
	must(err)
	steps := 0
	for {
		progress, err := store.StepRouting(ctx, "scale", op.ID)
		must(err)
		steps++
		if steps%500 == 0 {
			fmt.Printf("%s steps=%d elapsed=%s\n", c.Kind, steps, time.Since(begin))
		}
		if progress.Done {
			break
		}
	}
	stop()
	must(<-writerDone)
	got, err := (postgres.OperationStore{Pool: pool}).Operation(ctx, "scale", op.ID)
	must(err)
	result := map[string]any{"kind": c.Kind, "state": got.State, "seconds": time.Since(begin).Seconds(), "steps": steps, "counters": got.Counters, "lock_samples": stats.Samples, "max_lock_wait_ms": float64(stats.Max) / float64(time.Millisecond), "errors": got.Errors}
	raw, err := json.Marshal(result)
	must(err)
	fmt.Println(string(raw))
	if got.State != "succeeded" || stats.Samples == 0 || stats.Max >= 100*time.Millisecond {
		fmt.Fprintln(os.Stderr, "routing measurement failed")
		os.Exit(1)
	}
}
