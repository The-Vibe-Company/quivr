package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// Owns the synchronous routing probe's work bound. Background coverage counts
// have a separate owner; neither elapsed time nor forced planner settings can
// establish that a small Corpus avoids reading its large neighbour.
func TestVectorSpaceDescriptionWorkIsBoundedByCorpus(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	pool := queueWorkPool(t, ctx)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO corpora(organization,id,request_key,canonical_request,name,retrieval)
SELECT 'example',id,id,'',id,'{}' FROM unnest(ARRAY['neighbour','target','empty']) id;
INSERT INTO content_blobs(organization,blob_id,object_key,sha256,byte_length)
VALUES('example','blob','fixture','fixture',1);
INSERT INTO vector_spaces(id,manifest,owner_plugin_id,role)
VALUES('served','{}','example.owner','served'),('evaluation','{}','example.evaluation','evaluation');
INSERT INTO projection_generations(id,collection,profile_version,active,space_id,spaces_projected,spaces)
VALUES('generation','generation','fixture',true,'served',true,
 '[{"id":"served","role":"served","owner_plugin_id":"example.owner"},
   {"id":"evaluation","role":"evaluation","owner_plugin_id":"example.evaluation"}]')`)
	seed := func(corpus string, low, high int) {
		t.Helper()
		exec(`INSERT INTO records(organization,id,corpus_id,namespace,record_key,current_version_id)
SELECT 'example',$1||'-'||i,$1,'fixture',i::text,$1||'-current-'||i
FROM generate_series($2::int,$3::int) i`, corpus, low, high)
		exec(`INSERT INTO record_versions(organization,id,record_id,slot,digest,acceptance_order,
 source_position,text_blob_id,manifest_blob_id,provenance,baseline_ready)
SELECT 'example',$1||'-'||kind||'-'||i,$1||'-'||i,kind,i::text,1,'','blob','blob','{}',true
FROM generate_series($2::int,$3::int) i CROSS JOIN (VALUES('current'),('old')) k(kind)`, corpus, low, high)
		exec(`INSERT INTO segmentations(organization,id,version_id,recipe,digest)
SELECT organization,id,id,'plugin:example.owner@1.0.0',id FROM record_versions
WHERE organization='example' AND record_id IN
 (SELECT $1||'-'||i FROM generate_series($2::int,$3::int) i);
`, corpus, low, high)
		exec(`INSERT INTO projection_coverage(organization,version_id,generation_id,segmentation_id)
SELECT organization,id,'generation',id FROM record_versions
WHERE organization='example' AND record_id IN
 (SELECT $1||'-'||i FROM generate_series($2::int,$3::int) i)
AND ($1='neighbour' OR slot='old')`, corpus, low, high)
	}
	seed("neighbour", 1, 1000)
	seed("target", 1, 100)
	// The target's only current served projection is late in scan order.
	// Historical projections and evaluation-only cuts cannot serve that owner.
	exec(`INSERT INTO projection_coverage(organization,version_id,generation_id,segmentation_id)
VALUES('example','target-current-100','generation','target-current-100');
INSERT INTO segmentations(organization,id,version_id,recipe,digest)
SELECT 'example','evaluation-'||i,'target-current-'||i,'plugin:example.evaluation@1.0.0',i::text
FROM generate_series(1,100) i;
INSERT INTO projection_coverage(organization,version_id,generation_id,segmentation_id,role)
SELECT 'example','target-current-'||i,'generation','evaluation-'||i,'evaluation'
FROM generate_series(1,100) i;
UPDATE records SET withdrawn=true WHERE organization='example' AND id='target-1';
UPDATE record_versions SET quarantined=true WHERE organization='example' AND id='target-current-2';
UPDATE record_versions SET baseline_ready=false WHERE organization='example' AND id='target-current-3';
INSERT INTO tombstones(organization,record_id) VALUES('example','target-4')`)

	check := func(t *testing.T, corpus string, serving, eligible bool) {
		t.Helper()
		bound := float64(100)
		if corpus == "empty" {
			bound = 0
		}
		_, spaces, err := (SpaceStore{Pool: pool}).DescribeVectorSpaces(ctx, "example", corpus)
		if err != nil {
			t.Fatal(err)
		}
		if len(spaces) != 2 {
			t.Fatalf("corpus %s: got %d spaces, want 2", corpus, len(spaces))
		}
		for _, space := range spaces {
			want := int64(0)
			if space.ID == "served" && serving {
				want = 1
			}
			if space.ServingSegments == nil || *space.ServingSegments != want ||
				space.CorpusEmpty != (!serving && !eligible) || !space.CoverageUnknown {
				t.Fatalf("corpus %s: live description %+v, want presence %d and empty %t",
					corpus, space, want, !serving && !eligible)
			}
		}
		for _, owner := range []string{"example.owner", "example.evaluation", "example.absent"} {
			var present bool
			args := []any{"example", corpus, "generation", owner}
			if err := pool.QueryRow(ctx, spaceOwnerPresenceSQL, args...).Scan(&present); err != nil {
				t.Fatal(err)
			}
			if want := serving && owner == "example.owner"; present != want {
				t.Fatalf("corpus %s, owner %s: present %t, want %t", corpus, owner, present, want)
			}
			var raw []byte
			if err := pool.QueryRow(ctx, "EXPLAIN (ANALYZE, FORMAT JSON) "+spaceOwnerPresenceSQL, args...).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			assertSpaceDescriptionRowBound(t, raw, bound)
		}
		var raw []byte
		if err := pool.QueryRow(ctx, "EXPLAIN (ANALYZE, FORMAT JSON) "+corpusEligibilityPresenceSQL, "example", corpus).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		assertSpaceDescriptionRowBound(t, raw, bound)
	}
	for _, size := range []int{1000, 10000} {
		if size == 10000 {
			seed("neighbour", 1001, size)
			exec(`INSERT INTO tombstones(organization,record_id)
SELECT 'example','neighbour-'||i FROM generate_series(1,10000) i WHERE i%2=0`)
		}
		for _, statistics := range []string{"stale", "fresh"} {
			if statistics == "fresh" {
				exec("ANALYZE")
			}
			t.Run(fmt.Sprintf("neighbour-%d/%s", size, statistics), func(t *testing.T) {
				check(t, "target", true, true)
				check(t, "empty", false, false)
			})
		}
	}
	// Missing current served coverage leaves a nonempty Corpus rebuilding;
	// historical and evaluation coverage do not make the owner present.
	exec(`DELETE FROM projection_coverage WHERE organization='example' AND version_id='target-current-100' AND role='served'`)
	check(t, "target", false, true)
	// Coverage remains, but every current target Version is now ineligible.
	// A live description must change without any generation or cache change.
	exec(`INSERT INTO projection_coverage(organization,version_id,generation_id,segmentation_id)
VALUES('example','target-current-100','generation','target-current-100');
UPDATE record_versions SET baseline_ready=false WHERE organization='example' AND record_id LIKE 'target-%'`)
	check(t, "target", false, false)
}

func assertSpaceDescriptionRowBound(t *testing.T, raw []byte, bound float64) {
	t.Helper()
	type planNode struct {
		Kind        string     `json:"Node Type"`
		Relation    string     `json:"Relation Name"`
		Rows        float64    `json:"Actual Rows"`
		Loops       float64    `json:"Actual Loops"`
		Filtered    float64    `json:"Rows Removed by Filter"`
		Rechecked   float64    `json:"Rows Removed by Index Recheck"`
		IndexCond   string     `json:"Index Cond"`
		RecheckCond string     `json:"Recheck Cond"`
		Plans       []planNode `json:"Plans"`
	}
	var explained []struct{ Plan planNode }
	if err := json.Unmarshal(raw, &explained); err != nil || len(explained) != 1 {
		t.Fatalf("invalid EXPLAIN: %s (%v)", raw, err)
	}
	visited := map[string]float64{}
	var walk func(planNode)
	walk = func(node planNode) {
		switch node.Relation {
		case "records", "record_versions", "projection_coverage", "tombstones":
			if node.Relation != "tombstones" && node.Loops > 0 && (node.Kind == "Seq Scan" || (node.IndexCond == "" && node.RecheckCond == "")) {
				t.Fatalf("description scanned %s without an index condition: %s", node.Relation, raw)
			}
			visited[node.Relation] += (node.Rows + node.Filtered + node.Rechecked) * node.Loops
		}
		for _, child := range node.Plans {
			walk(child)
		}
	}
	walk(explained[0].Plan)
	for relation, rows := range visited {
		if rows > bound {
			t.Fatalf("description visited %.0f %s rows, target bound %.0f: %s", rows, relation, bound, raw)
		}
	}
}
