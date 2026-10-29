// Opt-in SQL measurement on an isolated, migrated PostgreSQL database.
//
// The ignored --config JSON must contain database_url for an isolated migrated
// database named hydration_restart_*. Bootstrap it with the adapter TestMain:
// QUIVR_ADAPTER_CONFIG=<config> go test ./internal/adapters/postgres -run '^$'
// Seed once with --config <config> --seed 1000. This also saves the exact 50
// candidates beside the configuration, before caches are cleared.
//
// Build this same harness against the base and candidate in separate worktrees.
// For each binary, stop the owned PostgreSQL instance, drop the OS cache where
// permitted (sync; echo 3 > /proc/sys/vm/drop_caches), then start it with
// pg_prewarm.autoprewarm=off. Run the binary with --config <config>. JSON reports
// elapsed SQL hydration time, shared page touches, physical reads and relation
// costs. Only root totals are additive; parent plans include child buffers.
//
// For restoration, start with shared_preload_libraries=pg_stat_statements,pg_prewarm
// and pg_prewarm.autoprewarm=on, run the binary once, call
// CREATE EXTENSION IF NOT EXISTS pg_prewarm; SELECT autoprewarm_dump_now(),
// stop, drop OS cache and start again. Wait for
// the asynchronous autoprewarm worker to finish before comparing restored
// reads. A dump can report that the file is busy while restoration is running.
// Record both immediate and completed-restoration results; do not infer an
// immediate latency guarantee from the setting alone.
//
// This measures the two production SQL statements, without canonical blob
// reads, a vector-index request, or a client keep-warm loop. The neutral seed
// has plugin routing and wide segment derivations, but no embedding files.
// Timing on a small local fixture is relative evidence, not a deployment SLO.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const organization = "hydration-after-restart"

func main() {
	config := flag.String("config", "", "ignored JSON file containing database_url; never print its contents")
	seed := flag.Int("seed", 0, "seed this many neutral records in an empty migrated measurement database")
	flag.Parse()
	if err := run(*config, *seed); err != nil {
		// Dependency errors may contain a connection URL. Keep diagnostics local.
		fmt.Fprintln(os.Stderr, "hydration measurement failed; check the isolated database and configuration")
		os.Exit(1)
	}
}

func run(path string, seed int) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var input struct {
		DatabaseURL string `json:"database_url"`
	}
	if err = json.Unmarshal(raw, &input); err != nil {
		return err
	}
	cfg, err := pgxpool.ParseConfig(input.DatabaseURL)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(cfg.ConnConfig.Database, "hydration_restart_") {
		return fmt.Errorf("isolated database required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	if seed > 0 {
		if err = seedRecords(ctx, pool, seed); err != nil {
			return err
		}
		candidates := make([]content.Candidate, 50)
		for i := range candidates {
			candidates[i] = content.Candidate{SegmentID: fmt.Sprintf("s%d", (i+1)*(seed/50)), GenerationID: "restart-generation"}
		}
		raw, err := json.Marshal(candidates)
		if err != nil {
			return err
		}
		if err = os.WriteFile(path+".candidates", raw, 0600); err != nil {
			return err
		}
		fmt.Println("seeded neutral hydration fixture")
		return nil
	}
	// Capture plans on a dedicated connection. Candidate selection runs on the
	// setup connection before restart, so it cannot warm the measured relations.
	var candidates []content.Candidate
	raw, err = os.ReadFile(path + ".candidates")
	if err != nil {
		return err
	}
	if err = json.Unmarshal(raw, &candidates); err != nil {
		return err
	}
	if len(candidates) != 50 {
		return fmt.Errorf("50 candidates required")
	}
	var plans []string
	cfg.MaxConns = 1
	cfg.ConnConfig.OnNotice = func(_ *pgconn.PgConn, notice *pgconn.Notice) { plans = append(plans, notice.Message) }
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, `LOAD 'auto_explain'; SET auto_explain.log_min_duration=0; SET auto_explain.log_analyze=on; SET auto_explain.log_buffers=on; SET auto_explain.log_timing=off; SET auto_explain.log_format=json; SET auto_explain.log_level=notice`)
		return err
	}
	measured, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return err
	}
	defer measured.Close()
	if err = measured.Ping(ctx); err != nil {
		return err
	}
	plans = nil
	store := postgres.ProjectionStore{Pool: measured}
	scope := corpus.Scope{Organization: organization, Corpora: []string{"*"}}
	start := time.Now()
	hydrated, err := store.Hydrate(ctx, scope, candidates)
	if err != nil {
		return err
	}
	checked, err := store.RecheckHydration(ctx, scope, candidates)
	if err != nil {
		return err
	}
	elapsed := time.Since(start).Seconds() * 1000
	if len(hydrated) != 50 || len(checked) != 50 {
		return fmt.Errorf("missing candidates")
	}
	// Report relation/index page work and the root total separately: parent
	// buffers include children, so recursively adding every node double-counts.
	total, reads := 0, 0
	relations := map[string]int{}
	details := []map[string]any{}
	for index, raw := range plans {
		at := strings.Index(raw, "{")
		if at < 0 {
			return fmt.Errorf("missing plan")
		}
		var plan struct{ Plan map[string]any }
		if err = json.Unmarshal([]byte(raw[at:]), &plan); err != nil {
			return err
		}
		pages := func(n map[string]any, key string) int { v, _ := n[key].(float64); return int(v) }
		total += pages(plan.Plan, "Shared Hit Blocks") + pages(plan.Plan, "Shared Read Blocks")
		reads += pages(plan.Plan, "Shared Read Blocks")
		phase := "metadata"
		if index == 1 {
			phase = "recheck"
		}
		var milliseconds float64
		if _, err = fmt.Sscanf(raw, "duration: %f ms", &milliseconds); err != nil {
			return err
		}
		details = append(details, map[string]any{"phase": phase, "sql_ms": milliseconds, "pages": pages(plan.Plan, "Shared Hit Blocks") + pages(plan.Plan, "Shared Read Blocks"), "read_pages": pages(plan.Plan, "Shared Read Blocks")})
		var walk func(map[string]any)
		walk = func(n map[string]any) {
			if name, ok := n["Relation Name"].(string); ok {
				relations[name] += pages(n, "Shared Hit Blocks") + pages(n, "Shared Read Blocks")
			}
			if children, ok := n["Plans"].([]any); ok {
				for _, child := range children {
					walk(child.(map[string]any))
				}
			}
		}
		walk(plan.Plan)
	}
	if len(plans) != 2 {
		return fmt.Errorf("expected two statements")
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"hits": 50, "statements": len(plans), "hydration_ms": elapsed, "pages": total, "read_pages": reads, "relation_pages_including_subplans": relations, "statement_details": details})
}

func seedRecords(ctx context.Context, pool *pgxpool.Pool, count int) error {
	var existing int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM records`).Scan(&existing); err != nil {
		return err
	}
	if existing != 0 || count < 50 || count > 10000 {
		return fmt.Errorf("empty database and 50..10000 records required")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	statements := []string{
		`INSERT INTO corpora(organization,id,request_key,canonical_request,name,retrieval) VALUES('hydration-after-restart','corpus','corpus','{}','Restart measurement','{}')`,
		`INSERT INTO projection_generations(id,collection,profile_version,space_id,organization,corpus_id,ingestion_routing,coverage_routed) SELECT 'restart-generation',collection,profile_version,space_id,'hydration-after-restart','corpus','{"default":"example.hydration"}',true FROM projection_generations WHERE active`,
		`INSERT INTO corpus_projection_routes(organization,corpus_id,generation_id) VALUES('hydration-after-restart','corpus','restart-generation')`,
		`INSERT INTO content_blobs(organization,blob_id,object_key,sha256,byte_length) VALUES('hydration-after-restart','text','fixture/text','fixture',1)`,
		`INSERT INTO records(organization,id,corpus_id,namespace,record_key,current_version_id,desired_version_id) SELECT 'hydration-after-restart','r'||i,'corpus','fixture',i::text,'v'||i,'v'||i FROM generate_series(1,$1::integer) i`,
		`INSERT INTO accepted_revisions(organization,record_id,slot,digest,version_id,acceptance_order,source_position,command,source_media_type) SELECT organization,id,'1','fixture',current_version_id,1,'','{}','text/plain' FROM records`,
		`INSERT INTO record_versions(organization,id,record_id,slot,digest,acceptance_order,source_position,text_blob_id,manifest_blob_id,provenance,baseline_ready,enrichment_state) SELECT organization,current_version_id,id,'1','fixture',1,'','text','text','{}',true,'idle' FROM records`,
		`INSERT INTO version_parts(organization,version_id,part_key,role,blob_id) SELECT organization,id,'body','body','text' FROM record_versions`,
		`INSERT INTO segmentations(organization,id,version_id,recipe,digest) SELECT organization,'s'||substring(id from 2),id,'plugin:example.hydration@1','fixture' FROM record_versions`,
		`INSERT INTO segments(organization,id,segmentation_id,version_id,part_key,start_offset,end_offset,text_sha256,derivation) SELECT organization,id,id,version_id,'body',0,1,'fixture',jsonb_build_object('model_input',(SELECT string_agg(md5(s.id||j::text),'') FROM generate_series(1,64) j)) FROM segmentations s`,
		`INSERT INTO projection_coverage(organization,version_id,generation_id,segmentation_id) SELECT organization,version_id,'restart-generation',id FROM segmentations`,
		`ANALYZE`,
	}
	for _, q := range statements {
		args := []any{}
		if strings.Contains(q, "$1") {
			args = append(args, count)
		}
		if _, err = tx.Exec(ctx, q, args...); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
