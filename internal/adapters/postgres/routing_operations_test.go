package postgres_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/app"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/routing"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type explainedRoutingPlan struct {
	kind  string
	pages int
}

func planHasRelation(node map[string]any, relation string) bool {
	if got, _ := node["Relation Name"].(string); got == relation {
		return true
	}
	children, _ := node["Plans"].([]any)
	for _, child := range children {
		if plan, ok := child.(map[string]any); ok && planHasRelation(plan, relation) {
			return true
		}
	}
	return false
}

func planPages(node map[string]any) int {
	hit, _ := node["Shared Hit Blocks"].(float64)
	read, _ := node["Shared Read Blocks"].(float64)
	return int(hit + read)
}

func routingPlanKind(node map[string]any) string {
	switch {
	case node["Node Type"] == "Limit" && planHasRelation(node, "records"):
		return "keyset"
	case planHasRelation(node, "segments"):
		return "gap"
	case planHasRelation(node, "record_versions") && planHasRelation(node, "records"):
		return "point"
	default:
		return ""
	}
}

func TestRoutingPromotionCountsWithoutExclusiveLock(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := scratchDatabase(t, ctx)
	if err := app.BootstrapDatabase(ctx, pool, nil); err != nil {
		t.Fatal(err)
	}
	f := newBackfillFixtureWithPool(t, ctx, pool, 1, 0, "example.routing")
	restoreRegistry(t, ctx, f.pool)
	if _, err := f.pool.Exec(ctx, `UPDATE projection_generations SET spaces=spaces||jsonb_build_array(jsonb_build_object('id',$2::text,'metric','cosine','role','evaluation','owner_plugin_id',(SELECT owner_plugin_id FROM vector_spaces WHERE id=$2))) WHERE id=$1`, f.generation.ID, f.target); err != nil {
		t.Fatal(err)
	}
	for _, seg := range f.segments[f.versions[0]].Segments {
		f.cover(t, ctx, f.versions[0], seg.ID, f.target)
	}
	// Keep the plan proof meaningful even when the shared adapter database has
	// few historical segments. These Versions have no current Record pointer,
	// so routing never treats them as eligible content; only the segment table
	// contributes unrelated history to the measured gap count.
	historyID := fmt.Sprintf("routing-history-%d", time.Now().UnixNano())
	historyRecord, historyVersion, historySeg := historyID+"-record", historyID+"-version", historyID+"-segmentation"
	historyTextBlob, historyManifestBlob := historyID+"-text", historyID+"-manifest"
	const historicalSegments = 20000
	for _, item := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO content_blobs(organization,blob_id,object_key,sha256,byte_length) VALUES($1,$2,$2,$2,1),($1,$3,$3,$3,1)`, []any{f.org, historyTextBlob, historyManifestBlob}},
		{`INSERT INTO records(organization,id,corpus_id,namespace,record_key) VALUES($1,$2,$3,'routing-history',$2)`, []any{f.org, historyRecord, f.corpusID}},
		{`INSERT INTO record_versions(organization,id,record_id,slot,digest,acceptance_order,source_position,text_blob_id,manifest_blob_id,provenance) VALUES($1,$2,$3,'history',$2,1,'',$4,$5,'{}')`, []any{f.org, historyVersion, historyRecord, historyTextBlob, historyManifestBlob}},
		{`INSERT INTO version_parts(organization,version_id,part_key,role,blob_id) VALUES($1,$2,'body','body',$3)`, []any{f.org, historyVersion, historyTextBlob}},
		{`INSERT INTO segmentations(organization,id,version_id,recipe,digest) VALUES($1,$2,$3,'plugin:routing-history@0.1.0',$2)`, []any{f.org, historySeg, historyVersion}},
		{`INSERT INTO segments(organization,id,segmentation_id,version_id,part_key,start_offset,end_offset,text_sha256) SELECT $1,$2||'-'||n,$2,$3,'body',n,n+1,$2 FROM generate_series(1,$4::int) n`, []any{f.org, historySeg, historyVersion, historicalSegments}},
	} {
		if _, err := f.pool.Exec(ctx, item.query, item.args...); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		for _, item := range []struct {
			query string
			args  []any
		}{
			{`DELETE FROM segments WHERE organization=$1 AND segmentation_id=$2`, []any{f.org, historySeg}},
			{`DELETE FROM segmentations WHERE organization=$1 AND id=$2`, []any{f.org, historySeg}},
			{`DELETE FROM version_parts WHERE organization=$1 AND version_id=$2`, []any{f.org, historyVersion}},
			{`DELETE FROM record_versions WHERE organization=$1 AND id=$2`, []any{f.org, historyVersion}},
			{`DELETE FROM records WHERE organization=$1 AND id=$2`, []any{f.org, historyRecord}},
			{`DELETE FROM content_blobs WHERE organization=$1 AND blob_id=ANY($2::text[])`, []any{f.org, []string{historyTextBlob, historyManifestBlob}}},
		} {
			if _, err := f.pool.Exec(cleanup, item.query, item.args...); err != nil {
				t.Errorf("remove routing plan history: %v", err)
			}
		}
	})
	store := postgres.RoutingStore{Pool: f.pool}
	op, err := store.AcceptRouting(ctx, f.org, routing.Command{Kind: routing.KindPromotion, Target: f.target, Key: "promotion"})
	if err != nil {
		t.Fatal(err)
	}
	if op.State != "queued" || op.Admin == nil {
		t.Fatalf("admitted %+v, want queued admin Operation", op)
	}
	// Keep the test's installation-wide epoch out of unrelated owner tests.
	var oldEpoch *string
	if err = f.pool.QueryRow(ctx, `SELECT (SELECT epoch FROM routing_switch_state)`).Scan(&oldEpoch); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if oldEpoch == nil {
			_, err = f.pool.Exec(context.Background(), `DELETE FROM routing_switch_state`)
		} else {
			_, err = f.pool.Exec(context.Background(), `UPDATE routing_switch_state SET epoch=$1`, *oldEpoch)
		}
		if err != nil {
			t.Error(err)
		}
	})
	for {
		if _, err = store.StepRouting(ctx, f.org, op.ID); err != nil {
			t.Fatal(err)
		}
		var phase string
		if err = f.pool.QueryRow(ctx, `SELECT phase FROM routing_operations WHERE id=$1`, op.ID).Scan(&phase); err != nil {
			t.Fatal(err)
		}
		if phase == "scan" {
			break
		}
		if phase == "done" {
			t.Fatal("promotion ended before the count")
		}
	}
	var plans []explainedRoutingPlan
	planConfig := f.pool.Config().Copy()
	planConfig.MaxConns = 1
	planConfig.ConnConfig.OnNotice = func(_ *pgconn.PgConn, notice *pgconn.Notice) {
		start := strings.IndexByte(notice.Message, '{')
		if start < 0 {
			return
		}
		var plan struct {
			Plan map[string]any `json:"Plan"`
		}
		if json.Unmarshal([]byte(notice.Message[start:]), &plan) == nil {
			if kind := routingPlanKind(plan.Plan); kind != "" {
				plans = append(plans, explainedRoutingPlan{kind: kind, pages: planPages(plan.Plan)})
			}
		}
	}
	planConfig.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, `LOAD 'auto_explain'; SET auto_explain.log_min_duration=0; SET auto_explain.log_analyze=on; SET auto_explain.log_timing=off; SET auto_explain.log_buffers=on; SET auto_explain.log_format=json; SET auto_explain.log_level=notice`)
		return err
	}
	explained, err := pgxpool.NewWithConfig(ctx, planConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer explained.Close()
	stepStore := postgres.RoutingStore{Pool: explained}
	block, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer block.Rollback(context.Background())
	if _, err = block.Exec(ctx, `LOCK TABLE record_versions IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := stepStore.StepRouting(ctx, f.org, op.ID); done <- err }()
	for {
		var waiting bool
		if err = f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT FROM pg_locks WHERE relation='record_versions'::regclass AND NOT granted)`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("count finished before blocked phase: %v", err)
		default:
		}
	}
	writer, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback(context.Background())
	var shared bool
	start := time.Now()
	if err = writer.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock_shared(642003)`).Scan(&shared); err != nil {
		t.Fatal(err)
	}
	if !shared || time.Since(start) > 100*time.Millisecond {
		t.Fatalf("shared writer during count: acquired=%v wait=%v", shared, time.Since(start))
	}
	if err = writer.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = block.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}

	// A rebuild admitted after staging must inherit this promotion too, even
	// though its target is not yet the Corpus's routed generation.
	if err = (postgres.ProjectionStore{Pool: f.pool}).BootstrapGeneration(ctx, "RoutingDefault", f.served); err != nil {
		t.Fatal(err)
	}
	late, err := (postgres.OperationStore{Pool: f.pool}).AcceptRebuild(ctx, f.org, f.corpusID, "late-rebuild", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	// Publish through the real content path after the initial record scan. The
	// observing operation must reconcile this Record before cutover; otherwise
	// a promotion can switch to a space with no target vectors for new content.
	scope := corpus.Scope{Organization: f.org, Actions: []string{"corpora:write", "content:write", "content:read"}, Corpora: []string{"*"}}
	contentService := content.Service{Submissions: f.store, Receipts: f.store, RecordStore: f.store, Versions: f.store, Materialization: f.store, Baseline: f.store}
	dirtyText := "Content published after the routing scan."
	dirtyKey := fmt.Sprintf("post-scan-%d", time.Now().UnixNano())
	dirtyReceipt, err := contentService.Accept(ctx, scope, content.Command{
		Key:     dirtyKey,
		Source:  content.Source{CorpusID: f.corpusID, Namespace: "routing", RecordKey: dirtyKey},
		Content: content.Text{Kind: "text", Text: dirtyText},
	})
	if err != nil {
		t.Fatal(err)
	}
	dirtyWork, _, err := f.store.Work(ctx, f.org, dirtyReceipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.Publish(ctx, dirtyWork, publication(
		content.Blob{Key: "fixture/routing-dirty-text", SHA256: "routing-dirty-text-" + dirtyKey, Size: int64(len(dirtyText))},
		content.Blob{Key: "fixture/routing-dirty-manifest", SHA256: "routing-dirty-manifest-" + dirtyKey, Size: 2},
	)); err != nil {
		t.Fatal(err)
	}
	dirtyVersion := content.Version{ID: dirtyWork.VersionID, RecordID: dirtyWork.RecordID, Manifest: content.Manifest{Parts: []content.Part{{
		Key: "body", Role: "body", Content: content.Text{Kind: "text", Text: dirtyText},
	}}}}
	dirtySeg, err := content.PluginSegmentation(f.org, dirtyVersion, f.segments[f.versions[0]].Recipe, json.RawMessage(`{}`), []content.SegmentInput{{PartKey: "body", Start: 0, End: len([]rune(dirtyText))}})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.SaveSegmentation(ctx, f.org, dirtySeg); err != nil {
		t.Fatal(err)
	}
	if err = f.store.Promote(ctx, f.org, dirtySeg, f.generation); err != nil {
		t.Fatal(err)
	}
	f.segments[dirtyVersion.ID] = dirtySeg
	var dirty bool
	if err = f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT FROM routing_dirty_records WHERE epoch=$1 AND organization=$2 AND record_id=$3)`, op.ID, f.org, dirtyWork.RecordID).Scan(&dirty); err != nil {
		t.Fatal(err)
	}
	if !dirty {
		t.Fatalf("published record %s was not marked dirty for observing operation %s", dirtyWork.RecordID, op.ID)
	}

	// The first cutover must refuse the missing target coverage discovered by
	// reconciliation, rather than silently publishing an incomplete route.
	for {
		progress, err := stepStore.StepRouting(ctx, f.org, op.ID)
		if err != nil {
			t.Fatal(err)
		}
		failed, err := (postgres.OperationStore{Pool: f.pool}).Operation(ctx, f.org, op.ID)
		if err != nil {
			t.Fatal(err)
		}
		if failed.State == "failed" {
			if failed.Counters["required_missing"] != 1 || failed.Counters["versions_missing"] != 1 || failed.Counters["segments_missing"] != len(dirtySeg.Segments) {
				t.Fatalf("dirty record coverage counters: %+v", failed.Counters)
			}
			if len(failed.Errors) != 1 || failed.Errors[0].Code != "coverage_incomplete" {
				t.Fatalf("dirty record refusal: %+v", failed.Errors)
			}
			break
		}
		if progress.Done {
			t.Fatal("promotion cut over despite missing target coverage")
		}
	}
	var firstLateSpace string
	if err = f.pool.QueryRow(ctx, `SELECT space_id FROM routing_generation_settings WHERE epoch=$1 AND generation_id=$2`, op.ID, late.TargetGenerationID).Scan(&firstLateSpace); err != nil || firstLateSpace != f.target {
		t.Fatalf("concurrent rebuild missed staging: space=%s (%v)", firstLateSpace, err)
	}
	firstPlans := append([]explainedRoutingPlan(nil), plans...)
	var keysetPlans, pointPlans, gapPlans []explainedRoutingPlan
	for _, plan := range firstPlans {
		switch plan.kind {
		case "keyset":
			keysetPlans = append(keysetPlans, plan)
		case "gap":
			gapPlans = append(gapPlans, plan)
		case "point":
			pointPlans = append(pointPlans, plan)
		}
	}
	maxPages := func(plans []explainedRoutingPlan) int {
		max := 0
		for _, plan := range plans {
			if plan.pages > max {
				max = plan.pages
			}
		}
		return max
	}
	keysetPages, pointPages, gapPages := maxPages(keysetPlans), maxPages(pointPlans), maxPages(gapPlans)
	t.Logf("routing plan pages: keyset=%d max=%d point=%d max=%d gap=%d max=%d", len(keysetPlans), keysetPages, len(pointPlans), pointPages, len(gapPlans), gapPages)
	if len(keysetPlans) == 0 || keysetPages > 128 {
		t.Fatalf("routing keyset page read %d pages across %d plans, want at most 128", keysetPages, len(keysetPlans))
	}
	if len(pointPlans) == 0 || pointPages > 64 {
		t.Fatalf("routing record point lookup read %d pages across %d plans, want at most 64", pointPages, len(pointPlans))
	}
	if len(gapPlans) == 0 || gapPages > 128 {
		t.Fatalf("routing segment gap count read %d pages across %d plans, want at most 128", gapPages, len(gapPlans))
	}
	for {
		progress, err := stepStore.StepRouting(ctx, f.org, op.ID)
		if err != nil {
			t.Fatal(err)
		}
		if progress.Done {
			break
		}
	}

	// Cover the same Version in the target space and retry with a new command
	// key. A successful retry proves the failed operation left the old route
	// intact and that the target coverage is the only missing prerequisite.
	for _, seg := range dirtySeg.Segments {
		f.cover(t, ctx, dirtyVersion.ID, seg.ID, f.target)
	}
	retry, err := store.AcceptRouting(ctx, f.org, routing.Command{Kind: routing.KindPromotion, Target: f.target, Key: "promotion-retry"})
	if err != nil {
		t.Fatal(err)
	}
	for {
		progress, err := stepStore.StepRouting(ctx, f.org, retry.ID)
		if err != nil {
			t.Fatal(err)
		}
		if progress.Done {
			break
		}
	}
	got, err := (postgres.OperationStore{Pool: f.pool}).Operation(ctx, f.org, retry.ID)
	if err != nil || got.State != "succeeded" {
		t.Fatalf("promotion result: %+v (%v)", got, err)
	}
	g, err := f.store.Generation(ctx, f.org, f.corpusID)
	if err != nil || g.SpaceID != f.target {
		t.Fatalf("generation %+v (%v), want served %s", g, err, f.target)
	}
	if !g.Serves(f.target) || g.Serves(f.served) {
		t.Fatalf("promotion roles %+v", g.Spaces)
	}
	if got.Admin.ServedSpaceID != f.target {
		t.Fatalf("result %+v", got.Admin)
	}
	var lateSpace string
	if err = f.pool.QueryRow(ctx, `SELECT space_id FROM routing_generation_settings WHERE epoch=$1 AND generation_id=$2`, retry.ID, late.TargetGenerationID).Scan(&lateSpace); err != nil || lateSpace != f.target {
		t.Fatalf("late rebuild was not staged: space=%s (%v)", lateSpace, err)
	}
	if _, err = f.pool.Exec(ctx, `UPDATE vector_spaces SET role='retired' WHERE id=$1`, f.target); err != nil {
		t.Fatal(err)
	}
	replayed, err := store.AcceptRouting(ctx, f.org, routing.Command{Kind: routing.KindPromotion, Target: f.target, Key: "promotion-retry"})
	if err != nil || replayed.ID != retry.ID || replayed.State != "succeeded" {
		t.Fatalf("retired target replay %+v: %v", replayed, err)
	}

}
