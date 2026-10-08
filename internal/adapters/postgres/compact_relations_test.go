package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// This owns the coverage access-path contract: a fixed rebuild batch must not
// scan unrelated compact coverage per segment. Lifecycle correctness remains
// with the rebuild and compact-storage adapter tests. Schema setup is included
// in the 30-second budget; the measured statements use no timing threshold.
func TestCompactCoverageLookupWorkIsBoundedBySegments(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := queueWorkPool(t, ctx)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := EnsureIndexes(ctx, pool); err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	seedQueueCorpus(t, ctx, pool, "example")
	exec(`INSERT INTO corpora(organization,id,request_key,canonical_request,name,retrieval) VALUES('example','unrelated','unrelated','','Unrelated','{}')`)
	exec(`INSERT INTO vector_spaces(id,manifest) VALUES('space','{}'),('other-space','{}');
INSERT INTO projection_generations(id,collection,profile_version,space_id,active)
VALUES('served','served','example','space',true),('target','target','example','space',false);
INSERT INTO storage_organizations(organization) VALUES('example'),('other');
INSERT INTO storage_spaces(space_id) VALUES('space'),('other-space')`)
	var firstBlocks int
	for _, size := range []int{1000, 10000} {
		low := 1
		if size == 10000 {
			low = 1001
		}
		exec(`INSERT INTO records(organization,id,corpus_id,namespace,record_key,current_version_id)
SELECT 'example','record-'||i,CASE WHEN i<=1000 THEN 'corpus' ELSE 'unrelated' END,'example',i::text,'version-'||lpad(i::text,8,'0') FROM generate_series($1::int,$2::int) i`, low, size)
		exec(`INSERT INTO record_versions(organization,id,record_id,slot,digest,acceptance_order,source_position,text_blob_id,manifest_blob_id,provenance,baseline_ready)
SELECT 'example','version-'||lpad(i::text,8,'0'),'record-'||i,'text',i::text,1,'','blob','blob','{}',true FROM generate_series($1::int,$2::int) i`, low, size)
		exec(`INSERT INTO version_parts(organization,version_id,part_key,role,blob_id)
SELECT 'example','version-'||lpad(i::text,8,'0'),'body','text','blob' FROM generate_series($1::int,$2::int) i`, low, size)
		exec(`INSERT INTO segmentations(organization,id,version_id,recipe,digest)
SELECT 'example','segmentation-'||i,'version-'||lpad(i::text,8,'0'),'text',i::text FROM generate_series($1::int,$2::int) i`, low, size)
		exec(`INSERT INTO segments(organization,id,segmentation_id,version_id,part_key,start_offset,end_offset,text_sha256)
SELECT 'example','segment-'||i,'segmentation-'||i,'version-'||lpad(i::text,8,'0'),'body',0,1,'example' FROM generate_series($1::int,$2::int) i`, low, size)
		exec(`INSERT INTO storage_segments(organization_id,segment_id)
SELECT o.id,'segment-'||i FROM storage_organizations o CROSS JOIN generate_series($1::int,$2::int) i WHERE o.organization='example'`, low, size)
		exec(`INSERT INTO compact_embeddings(organization_id,segment_id,space_id,file_id,ordinal,vector_sha256,artifact_sha256)
SELECT k.organization_id,k.id,sp.id,i,0,sha256('vector'),sha256(convert_to('compact-'||i,'UTF8'))
FROM generate_series($1::int,$2::int) i JOIN storage_segments k ON k.segment_id='segment-'||i
JOIN storage_organizations o ON o.id=k.organization_id AND o.organization='example'
CROSS JOIN storage_spaces sp WHERE sp.space_id='space' AND i<>2`, low, size)
		exec(`INSERT INTO compact_embedding_coverage(organization_id,file_id,generation_id,covered)
SELECT o.id,i,'served',CASE WHEN i=4 THEN decode('00','hex') ELSE decode('01','hex') END
FROM storage_organizations o CROSS JOIN generate_series($1::int,$2::int) i WHERE o.organization='example' AND i<>2`, low, size)
		if size == 1000 {
			exec(`INSERT INTO compact_embedding_coverage(organization_id,file_id,generation_id,covered)
SELECT id,1,'target',decode('01','hex') FROM storage_organizations WHERE organization='example';
INSERT INTO compact_embeddings(organization_id,segment_id,space_id,file_id,ordinal,vector_sha256,artifact_sha256)
SELECT k.organization_id,k.id,sp.id,100001,0,sha256('vector'),sha256('other-space')
FROM storage_segments k JOIN storage_organizations o ON o.id=k.organization_id AND o.organization='example'
CROSS JOIN storage_spaces sp WHERE k.segment_id='segment-1' AND sp.space_id='other-space';
INSERT INTO compact_embedding_coverage(organization_id,file_id,generation_id,covered)
SELECT id,100001,'served',decode('01','hex') FROM storage_organizations WHERE organization='example';
INSERT INTO storage_segments(organization_id,segment_id) SELECT id,'segment-1' FROM storage_organizations WHERE organization='other';
INSERT INTO compact_embeddings(organization_id,segment_id,space_id,file_id,ordinal,vector_sha256,artifact_sha256)
SELECT k.organization_id,k.id,sp.id,100002,0,sha256('vector'),sha256('other-organization')
FROM storage_segments k JOIN storage_organizations o ON o.id=k.organization_id AND o.organization='other'
CROSS JOIN storage_spaces sp WHERE sp.space_id='space';
INSERT INTO compact_embedding_coverage(organization_id,file_id,generation_id,covered)
SELECT id,100002,'served',decode('01','hex') FROM storage_organizations WHERE organization='other';
INSERT INTO projection_coverage(organization,version_id,generation_id,segmentation_id)
SELECT 'example','version-'||lpad(i::text,8,'0'),'target','segmentation-'||i FROM generate_series(1,128) i`)
		}
		exec("ANALYZE")
		var raw []byte
		if err := pool.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+rebuildCandidatesSQL, "example", "corpus", "target", 128, "").Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var plans []struct{ Plan map[string]any }
		if err := json.Unmarshal(raw, &plans); err != nil {
			t.Fatal(err)
		}
		indexed := map[string]bool{}
		var visit func(map[string]any)
		visit = func(node map[string]any) {
			relation, _ := node["Relation Name"].(string)
			condition, _ := node["Index Cond"].(string)
			switch relation {
			case "storage_segments":
				if strings.Contains(condition, "segment_id") && strings.Contains(condition, "sg.id") {
					indexed[relation] = true
				}
			case "compact_embedding_coverage":
				if node["Node Type"] == "Seq Scan" {
					t.Fatalf("rebuild scanned unrelated compact coverage: %s", raw)
				}
				if strings.Contains(condition, "organization_id") && strings.Contains(condition, "file_id") {
					indexed[relation] = true
				}
			}
			children, _ := node["Plans"].([]any)
			for _, child := range children {
				visit(child.(map[string]any))
			}
		}
		visit(plans[0].Plan)
		for _, relation := range []string{"storage_segments", "compact_embedding_coverage"} {
			if !indexed[relation] {
				t.Fatalf("missing per-segment index lookup through %s: %s", relation, raw)
			}
		}
		blocks := queueWorkBlocks(t, raw)
		t.Logf("coverage rows=%d: first 128 rebuild candidates use %d shared buffers", size, blocks)
		if size == 1000 {
			firstBlocks = blocks
		} else if blocks > 2*firstBlocks+100 {
			t.Fatalf("fixed rebuild batch grew with unrelated coverage: buffers %d -> %d; plan: %s", firstBlocks, blocks, raw)
		}
	}
	rows, err := pool.Query(ctx, `SELECT sg.id,ec.generation_id,ec.space_id,ec.artifact_id
FROM segments sg JOIN LATERAL `+embeddingCoverageForSegmentSQL("sg.organization", "sg.id")+` ec ON ec.organization=sg.organization AND ec.segment_id=sg.id
WHERE sg.organization='example' AND sg.id=ANY(ARRAY['segment-1','segment-2','segment-3','segment-4'])
ORDER BY sg.id,ec.generation_id,ec.space_id,ec.artifact_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var segment, generation, space, artifact string
		if err := rows.Scan(&segment, &generation, &space, &artifact); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%s/%s/%s/%s", segment, generation, space, artifact))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := "segment-1/served/other-space/f3e23ba78cef348461271539add8a2772b2119f60974ed33e80f157523b2580e\n" +
		"segment-1/served/space/b4ef762527f9a70a7c60a237a5c316a29a45391d6326d74074a5a1fa5dd60be0\n" +
		"segment-1/target/space/b4ef762527f9a70a7c60a237a5c316a29a45391d6326d74074a5a1fa5dd60be0\n" +
		"segment-3/served/space/" + "c212991eb256ebdd9d1bcc9f15ae983221cfba7242b69742a8765cd6702064d9"
	if strings.Join(got, "\n") != want {
		t.Fatalf("compact coverage changed bits, spaces, generations or organization: got %q, want %q", got, want)
	}
}
