package postgres

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// segmentWriteTracer counts SQL statements and network roundtrips via pgx's
// QueryTracer and BatchTracer hooks. It distinguishes data statements from
// transaction-control and version/journal lock bookkeeping.
type segmentWriteTracer struct {
	mu         sync.Mutex
	dataStmts  int64
	totalStmts int64
	roundtrips int64
}

func (t *segmentWriteTracer) Reset() {
	t.mu.Lock()
	t.dataStmts = 0
	t.totalStmts = 0
	t.roundtrips = 0
	t.mu.Unlock()
}

func (t *segmentWriteTracer) Snapshot() (dataStmts, totalStmts, roundtrips int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.dataStmts, t.totalStmts, t.roundtrips
}

func isTxControlSQL(sql string) bool {
	s := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(sql), ";")))
	return s == "begin" || s == "commit" || s == "rollback" ||
		strings.HasPrefix(s, "savepoint ") ||
		strings.HasPrefix(s, "release savepoint ") ||
		strings.HasPrefix(s, "rollback to savepoint ") ||
		strings.HasPrefix(s, "deallocate ")
}

func isLockOnlySQL(sql string) bool {
	s := strings.ToLower(strings.Join(strings.Fields(sql), " "))
	return strings.Contains(s, "pg_advisory_xact_lock") ||
		strings.Contains(s, "insert into journal_request_claims") ||
		(strings.Contains(s, "from journal_request_claims ") && strings.Contains(s, "for update")) ||
		(strings.HasPrefix(s, "select id from record_versions ") && strings.HasSuffix(s, "for update")) ||
		(strings.HasPrefix(s, "select r.id from records r ") && strings.HasSuffix(s, "for update of r"))
}

func (t *segmentWriteTracer) recordStmt(sql string, isBatchMember bool) {
	if isTxControlSQL(sql) {
		return
	}
	t.mu.Lock()
	t.totalStmts++
	if !isLockOnlySQL(sql) {
		t.dataStmts++
	}
	if !isBatchMember {
		t.roundtrips++
	}
	t.mu.Unlock()
}

func (t *segmentWriteTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	t.recordStmt(data.SQL, false)
	return ctx
}

func (*segmentWriteTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (t *segmentWriteTracer) TraceBatchStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceBatchStartData) context.Context {
	t.mu.Lock()
	t.roundtrips++
	t.mu.Unlock()
	return ctx
}

func (t *segmentWriteTracer) TraceBatchQuery(_ context.Context, _ *pgx.Conn, data pgx.TraceBatchQueryData) {
	t.recordStmt(data.SQL, true)
}

func (*segmentWriteTracer) TraceBatchEnd(context.Context, *pgx.Conn, pgx.TraceBatchEndData) {}

// TestSegmentWriteUnnestSQLContracts owns the offline SQL structure invariants
// for batched segment, compact-embedding, and compact-coverage writes: unnest
// vectorization, deterministic ascending lock order, and idempotent conflict
// handling.
func TestSegmentWriteUnnestSQLContracts(t *testing.T) {
	checks := []struct {
		name      string
		sql       string
		fragments []string
	}{
		{
			name: "saveSegmentationSQL",
			sql:  saveSegmentationSQL,
			fragments: []string{
				"INSERT INTO segmentations(organization,id,version_id,recipe,digest,provenance,segment_count)",
				"ON CONFLICT(organization,id) DO NOTHING",
				"INSERT INTO segments(organization,id,segmentation_id,version_id,part_key,start_offset,end_offset,text_sha256,derivation)",
				"CROSS JOIN unnest($8::text[],$9::text[],$10::integer[],$11::integer[],$12::text[],$13::jsonb[])",
				"WITH ORDINALITY AS x(id,part_key,start_offset,end_offset,text_sha256,derivation,pos)",
				"ORDER BY x.id, x.pos",
			},
		},
		{
			name: "internEmbeddingKeysSQL",
			sql:  internEmbeddingKeysSQL,
			fragments: []string{
				"INSERT INTO vector_spaces(id,manifest) VALUES($1,$2) ON CONFLICT DO NOTHING",
				"INSERT INTO storage_organizations(organization) VALUES($3) ON CONFLICT DO NOTHING",
				"INSERT INTO storage_spaces(space_id) VALUES($4) ON CONFLICT DO NOTHING",
			},
		},
		{
			name: "lookupEmbeddingGroupStateSQL",
			sql:  lookupEmbeddingGroupStateSQL,
			fragments: []string{
				"SELECT vs.manifest=$2::jsonb",
				"FROM vector_spaces vs",
				"CROSS JOIN storage_organizations o",
				"CROSS JOIN storage_spaces s",
			},
		},
		{
			name: "upsertEmbeddingFileAndSegmentsSQL",
			sql:  upsertEmbeddingFileAndSegmentsSQL,
			fragments: []string{
				"INSERT INTO embedding_files(",
				"ON CONFLICT(organization_id,segmentation_id,space_id) DO UPDATE",
				"INSERT INTO storage_segments(organization_id,segment_id)",
				"CROSS JOIN unnest($14::text[]) AS x(segment_id)",
				"ORDER BY x.segment_id",
				"ON CONFLICT(organization_id,segment_id) DO NOTHING",
			},
		},
		{
			name: "insertCompactEmbeddingsSQL",
			sql:  insertCompactEmbeddingsSQL,
			fragments: []string{
				"INSERT INTO compact_embeddings(organization_id,segment_id,space_id,file_id,ordinal,vector_sha256,artifact_sha256)",
				"FROM unnest($2::text[],$5::integer[],$6::bytea[],$7::bytea[])",
				"WITH ORDINALITY AS x(segment_id,ordinal,vector_sha256,artifact_sha256,pos)",
				"JOIN storage_segments k ON k.organization_id=$1 AND k.segment_id=x.segment_id",
				"ORDER BY k.id, x.pos",
				"ON CONFLICT(organization_id,segment_id,space_id) DO NOTHING",
			},
		},
		{
			name: "validateCompactCoverageSQL",
			sql:  validateCompactCoverageSQL,
			fragments: []string{
				"FROM unnest($1::bigint[],$2::integer[],$3::text[],$4::text[])",
				"WITH ORDINALITY AS req(file_id,ordinal,segment_id,space_id,ord)",
				"ORDER BY req.ord",
			},
		},
		{
			name: "lockCompactCoverageFilesSQL",
			sql:  lockCompactCoverageFilesSQL,
			fragments: []string{
				"FROM embedding_files",
				"WHERE id=ANY($1::bigint[])",
				"ORDER BY id",
				"FOR UPDATE",
			},
		},
		{
			name: "loadCompactCoverageBatchSQL",
			sql:  loadCompactCoverageBatchSQL,
			fragments: []string{
				"FROM unnest($1::bigint[],$2::bigint[]) AS req(organization_id,file_id)",
				"JOIN compact_embedding_coverage c ON c.organization_id=req.organization_id AND c.file_id=req.file_id AND c.generation_id=$3",
			},
		},
		{
			name: "upsertCompactCoverageBatchSQL",
			sql:  upsertCompactCoverageBatchSQL,
			fragments: []string{
				"INSERT INTO compact_embedding_coverage(organization_id,file_id,generation_id,covered)",
				"FROM unnest($1::bigint[],$2::bigint[],$4::bytea[]) AS x(organization_id,file_id,covered)",
				"ORDER BY x.organization_id,x.file_id",
				"ON CONFLICT(organization_id,file_id,generation_id) DO UPDATE SET covered=EXCLUDED.covered",
			},
		},
	}
	for _, tc := range checks {
		for _, frag := range tc.fragments {
			if !strings.Contains(tc.sql, frag) {
				t.Errorf("%s missing expected clause %q", tc.name, frag)
			}
		}
	}
}

func TestPrepareSegmentInsertBatchBuildsAlignedArraysWithoutMutatingInput(t *testing.T) {
	empty, err := prepareSegmentInsertBatch(nil)
	if err != nil {
		t.Fatal(err)
	}
	if empty.IDs == nil || empty.PartKeys == nil || empty.Starts == nil || empty.Ends == nil || empty.TextSHA256s == nil || empty.Derivations == nil {
		t.Fatalf("empty batch must use non-nil slices for pgx unnest: %#v", empty)
	}

	input := []content.Segment{
		{ID: "seg-c", PartKey: "body", Start: 20, End: 30, Text: "gamma", Derivation: content.SegmentDerivation{Ordinal: 2, ModelInputSHA256: "m2"}},
		{ID: "seg-a", PartKey: "body", Start: 0, End: 10, Text: "alpha", Derivation: content.SegmentDerivation{Ordinal: 0, ModelInputSHA256: "m0"}},
		{ID: "seg-b", PartKey: "head", Start: 10, End: 20, Text: "beta", Derivation: content.SegmentDerivation{Ordinal: 1, ModelInputSHA256: "m1"}},
	}
	origOrder := []string{input[0].ID, input[1].ID, input[2].ID}

	batch, err := prepareSegmentInsertBatch(input)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{input[0].ID, input[1].ID, input[2].ID}; !slices.Equal(got, origOrder) {
		t.Fatalf("caller slice mutated: got %v, want %v", got, origOrder)
	}
	// Input order is preserved so WITH ORDINALITY tie-breaks duplicate IDs
	// deterministically while SQL ORDER BY x.id, x.pos enforces DB-collation
	// primary-key lock order.
	if !slices.Equal(batch.IDs, origOrder) {
		t.Fatalf("IDs = %v, want %v", batch.IDs, origOrder)
	}
	if !slices.Equal(batch.PartKeys, []string{"body", "body", "head"}) ||
		!slices.Equal(batch.Starts, []int{20, 0, 10}) ||
		!slices.Equal(batch.Ends, []int{30, 10, 20}) {
		t.Fatalf("columns misaligned: %+v", batch)
	}
	wantHashes := []string{content.Hash([]byte("gamma")), content.Hash([]byte("alpha")), content.Hash([]byte("beta"))}
	if !slices.Equal(batch.TextSHA256s, wantHashes) {
		t.Fatalf("TextSHA256s = %v, want %v", batch.TextSHA256s, wantHashes)
	}
	for i, seg := range input {
		wantJSON, err := compactSegmentDerivation(seg.Derivation)
		if err != nil {
			t.Fatal(err)
		}
		if batch.Derivations[i] != string(wantJSON) {
			t.Fatalf("derivation[%d] = %s, want %s", i, batch.Derivations[i], wantJSON)
		}
	}
}

func TestPrepareCompactEmbeddingBatchSortsDeduplicatesAndValidatesDigests(t *testing.T) {
	empty, err := prepareCompactEmbeddingBatch(nil)
	if err != nil {
		t.Fatal(err)
	}
	if empty.UniqueSegmentIDs == nil || empty.SegmentIDs == nil || empty.Ordinals == nil || empty.VectorSHAs == nil || empty.ArtifactSHAs == nil {
		t.Fatalf("empty compact batch must use non-nil slices: %#v", empty)
	}

	v0 := content.Hash([]byte("v0"))
	v1 := content.Hash([]byte("v1"))
	v2 := content.Hash([]byte("v2"))
	a0 := content.Hash([]byte("a0"))
	a1 := content.Hash([]byte("a1"))
	a2 := content.Hash([]byte("a2"))

	input := []content.Embedding{
		{ID: a2, SegmentID: "seg-2", Ordinal: 2, Payload: content.Blob{SHA256: v2}},
		{ID: a0, SegmentID: "seg-0", Ordinal: 0, Payload: content.Blob{SHA256: v0}},
		{ID: a1, SegmentID: "seg-1", Ordinal: 1, Payload: content.Blob{SHA256: v1}},
		{ID: a1, SegmentID: "seg-1", Ordinal: 9, Payload: content.Blob{SHA256: v1}},
	}
	batch, err := prepareCompactEmbeddingBatch(input)
	if err != nil {
		t.Fatal(err)
	}
	if input[0].SegmentID != "seg-2" || input[1].SegmentID != "seg-0" {
		t.Fatalf("caller artifacts slice mutated: %+v", input)
	}
	if !slices.Equal(batch.UniqueSegmentIDs, []string{"seg-0", "seg-1", "seg-2"}) {
		t.Fatalf("UniqueSegmentIDs = %v, want deduplicated sorted order", batch.UniqueSegmentIDs)
	}
	if !slices.Equal(batch.SegmentIDs, []string{"seg-0", "seg-1", "seg-1", "seg-2"}) ||
		!slices.Equal(batch.Ordinals, []int{0, 1, 9, 2}) {
		t.Fatalf("stably sorted batch = %+v", batch)
	}
	for i, wantHex := range []string{v0, v1, v1, v2} {
		if hex.EncodeToString(batch.VectorSHAs[i]) != wantHex {
			t.Fatalf("VectorSHAs[%d] mismatch", i)
		}
	}

	if _, err = prepareCompactEmbeddingBatch([]content.Embedding{{ID: "not-hex", SegmentID: "s", Payload: content.Blob{SHA256: v0}}}); !errors.Is(err, content.ErrInvalid) {
		t.Fatalf("bad artifact ID err = %v, want ErrInvalid", err)
	}
	if _, err = prepareCompactEmbeddingBatch([]content.Embedding{{ID: a0, SegmentID: "s", Payload: content.Blob{SHA256: "abcd"}}}); !errors.Is(err, content.ErrInvalid) {
		t.Fatalf("short vector SHA err = %v, want ErrInvalid", err)
	}
}

func TestPrepareCompactCoverageBatchValidatesAndMergesBitmaps(t *testing.T) {
	seg := content.Segmentation{ID: "seg-id", VersionID: "ver-id"}
	g := content.Generation{ID: "gen-id", SpaceID: "space-a", Spaces: []content.GenerationSpace{{ID: "space-a"}, {ID: "space-b"}}, SpacesProjected: true}
	f2 := &content.EmbeddingFile{ID: 20, RowCount: 10}
	f1 := &content.EmbeddingFile{ID: 10, RowCount: 10}

	artifacts := []content.Embedding{
		{Organization: "org", VersionID: "ver-id", SegmentationID: "seg-id", SegmentID: "s1", SpaceID: "space-b", Ordinal: 9, File: f2},
		{Organization: "org", VersionID: "ver-id", SegmentationID: "seg-id", SegmentID: "s0", SpaceID: "space-a", Ordinal: 0, File: f1},
		{Organization: "org", VersionID: "ver-id", SegmentationID: "seg-id", SegmentID: "s2", SpaceID: "space-b", Ordinal: 1, File: f2},
	}
	batch, err := prepareCompactCoverageBatch("org", seg, g, artifacts)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(batch.SortedFileIDs, []int64{10, 20}) {
		t.Fatalf("SortedFileIDs = %v, want [10 20]", batch.SortedFileIDs)
	}
	if !content.Present(batch.ByFile[10], 0) || !content.Present(batch.ByFile[20], 1) || !content.Present(batch.ByFile[20], 9) {
		t.Fatalf("unexpected coverage masks: %x %x", batch.ByFile[10], batch.ByFile[20])
	}

	merged, delta, err := mergeCoverageBitmap(batch.ByFile[20], []byte{0x02, 0x00}, 10)
	if err != nil || delta != 1 || !content.Present(merged, 1) || !content.Present(merged, 9) {
		t.Fatalf("mergeCoverageBitmap = %x delta=%d err=%v, want delta=1 with bits 1 and 9 set", merged, delta, err)
	}
	if _, _, err = mergeCoverageBitmap(batch.ByFile[20], []byte{0x01}, 10); !errors.Is(err, content.ErrConflict) {
		t.Fatalf("mismatched bitmap length err = %v, want ErrConflict", err)
	}

	bad := []content.Embedding{
		{Organization: "org", VersionID: "ver-id", SegmentationID: "seg-id", SegmentID: "s0", SpaceID: "space-a", Ordinal: 0, File: &content.EmbeddingFile{ID: 10, RowCount: 8}},
		{Organization: "org", VersionID: "ver-id", SegmentationID: "seg-id", SegmentID: "s1", SpaceID: "space-a", Ordinal: 1, File: &content.EmbeddingFile{ID: 10, RowCount: 16}},
	}
	if _, err = prepareCompactCoverageBatch("org", seg, g, bad); !errors.Is(err, content.ErrInvalid) {
		t.Fatalf("conflicting file RowCount err = %v, want ErrInvalid", err)
	}
}

func TestSegmentWriteTracerCountsStatementsAndRoundtrips(t *testing.T) {
	tr := &segmentWriteTracer{}
	ctx := context.Background()

	tr.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "begin"})
	tr.TraceBatchStart(ctx, nil, pgx.TraceBatchStartData{})
	tr.TraceBatchQuery(ctx, nil, pgx.TraceBatchQueryData{SQL: "SELECT pg_advisory_xact_lock_shared($1)"})
	tr.TraceBatchQuery(ctx, nil, pgx.TraceBatchQueryData{SQL: "SELECT id FROM record_versions WHERE organization=$1 AND id=$2 FOR UPDATE"})
	tr.TraceBatchQuery(ctx, nil, pgx.TraceBatchQueryData{SQL: "SELECT c.request_key FROM journal_request_claims c JOIN record_versions v ON (v.organization,v.record_id)=(c.organization,c.request_key) WHERE v.organization=$1 AND v.id=$2 AND c.family='record' FOR UPDATE OF c"})
	tr.TraceBatchEnd(ctx, nil, pgx.TraceBatchEndData{})
	tr.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: saveSegmentationSQL})
	tr.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "UPDATE record_versions SET segmented_at=clock_timestamp() WHERE organization=$1 AND id=$2"})
	tr.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "commit"})

	data, total, trips := tr.Snapshot()
	if data != 2 || total != 5 || trips != 3 {
		t.Fatalf("snapshot = (data=%d, total=%d, trips=%d), want (2, 5, 3)", data, total, trips)
	}
	tr.Reset()
	data, total, trips = tr.Snapshot()
	if data != 0 || total != 0 || trips != 0 {
		t.Fatalf("after Reset = (%d, %d, %d), want (0, 0, 0)", data, total, trips)
	}
}

func segmentWritePool(tb testing.TB, ctx context.Context, maxConns int32, tracer pgx.QueryTracer) *pgxpool.Pool {
	tb.Helper()
	configPath := os.Getenv("QUIVR_ADAPTER_CONFIG")
	if configPath == "" {
		tb.Skip("real PostgreSQL suite runs inside make adapter-postgres")
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		tb.Fatal(err)
	}
	var config struct {
		DatabaseURL string `json:"database_url"`
	}
	if err = json.Unmarshal(raw, &config); err != nil {
		tb.Fatal(err)
	}
	admin, err := pgxpool.New(ctx, config.DatabaseURL)
	if err != nil {
		tb.Fatal(err)
	}
	suffix := make([]byte, 6)
	if _, err = rand.Read(suffix); err != nil {
		admin.Close()
		tb.Fatal(err)
	}
	name := "seg_write_" + hex.EncodeToString(suffix)
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		admin.Close()
		tb.Fatal(err)
	}
	poolCfg, err := pgxpool.ParseConfig(config.DatabaseURL)
	if err != nil {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		admin.Close()
		tb.Fatal(err)
	}
	poolCfg.ConnConfig.Database = name
	poolCfg.MaxConns = maxConns
	if tracer != nil {
		poolCfg.ConnConfig.Tracer = tracer
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		admin.Close()
		tb.Fatal(err)
	}
	tb.Cleanup(func() {
		pool.Close()
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanupCtx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			tb.Errorf("drop %s: %v", name, err)
		}
		admin.Close()
	})
	if err = Migrate(ctx, pool); err != nil {
		tb.Fatal(err)
	}
	return pool
}

func seedSegmentWriteVersion(tb testing.TB, ctx context.Context, pool *pgxpool.Pool, org, corpusID, recordID, versionID string) {
	tb.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO organization_journals(organization) VALUES($1) ON CONFLICT DO NOTHING`, org); err != nil {
		tb.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO corpora(organization,id,request_key,canonical_request,name,retrieval)
 VALUES($1,$2,$2,decode('','hex'),'Segment Bench','{}') ON CONFLICT DO NOTHING`, org, corpusID); err != nil {
		tb.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO content_blobs(organization,blob_id,object_key,sha256,byte_length)
 VALUES($1,'blob-text','bench-text','bench-text',1),($1,'blob-manifest','bench-manifest','bench-manifest',1)
 ON CONFLICT DO NOTHING`, org); err != nil {
		tb.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO records(organization,id,corpus_id,namespace,record_key)
 VALUES($1,$2,$3,'bench',$2) ON CONFLICT DO NOTHING`, org, recordID, corpusID); err != nil {
		tb.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO record_versions(organization,id,record_id,slot,digest,acceptance_order,source_position,text_blob_id,manifest_blob_id,provenance,materialized_at)
 VALUES($1,$2,$3,'text',$2,1,'','blob-text','blob-manifest','{}',now()) ON CONFLICT DO NOTHING`, org, versionID, recordID); err != nil {
		tb.Fatal(err)
	}
}

func buildTestSegmentation(org, versionID, recipe string, n int) content.Segmentation {
	segID := content.StableID("segmentation", org, versionID, recipe)
	segments := make([]content.Segment, n)
	for i := range n {
		text := fmt.Sprintf("segment-%04d-payload-%s", i, recipe)
		normSHA := content.Hash([]byte(text))
		inputSHA := content.Hash([]byte("input:" + text))
		// Construct IDs in reverse ordinal suffix order so inputs are genuinely
		// unsorted before prepareSegmentInsertBatch runs.
		id := fmt.Sprintf("%s-seg-%04d", segID, n-1-i)
		segments[i] = content.Segment{
			ID:      id,
			PartKey: "body",
			Start:   i * 32,
			End:     (i + 1) * 32,
			Text:    text,
			Derivation: content.SegmentDerivation{
				Ordinal:          i,
				UTF8Start:        i * 32,
				UTF8End:          (i + 1) * 32,
				NormalizedSHA256: normSHA,
				ModelInputSHA256: inputSHA,
				ModelTokens:      8,
			},
		}
	}
	return content.Segmentation{
		ID:         segID,
		VersionID:  versionID,
		Recipe:     recipe,
		Segments:   segments,
		Provenance: json.RawMessage(`{"producer":"bench"}`),
	}
}

func buildTestEmbeddings(org, corpusID string, seg content.Segmentation, space content.VectorSpace, producer string) (content.EmbeddingFile, []content.Embedding) {
	n := len(seg.Segments)
	presence := make([]byte, (n+7)/8)
	artifacts := make([]content.Embedding, n)
	for i, p := range seg.Segments {
		content.MarkPresent(presence, i)
		vsha := content.Hash([]byte(fmt.Sprintf("vec:%s:%s:%d", space.ID, p.ID, i)))
		asha := content.Hash([]byte(fmt.Sprintf("art:%s:%s:%d", space.ID, p.ID, i)))
		artifacts[i] = content.Embedding{
			ID:             asha,
			Organization:   org,
			CorpusID:       corpusID,
			VersionID:      seg.VersionID,
			SegmentationID: seg.ID,
			SegmentID:      p.ID,
			PartKey:        p.PartKey,
			Recipe:         seg.Recipe,
			SpaceID:        space.ID,
			Producer:       producer,
			Ordinal:        i,
			SliceSHA:       content.Hash([]byte(p.Text)),
			SourceSHA:      p.Derivation.NormalizedSHA256,
			InputSHA:       p.Derivation.ModelInputSHA256,
			DerivationID:   content.StableID("embedding-derivation", org, p.ID, space.ID, p.Derivation.ModelInputSHA256, producer),
			Payload:        content.Blob{SHA256: vsha, Size: int64(4 * space.Dimensions)},
		}
	}
	fileSHA := content.Hash([]byte(fmt.Sprintf("file:%s:%s:%d", seg.ID, space.ID, n)))
	f := content.EmbeddingFile{
		Organization:   org,
		CorpusID:       corpusID,
		VersionID:      seg.VersionID,
		SegmentationID: seg.ID,
		Recipe:         seg.Recipe,
		SpaceID:        space.ID,
		Producer:       producer,
		Dimensions:     space.Dimensions,
		RowCount:       n,
		Presence:       presence,
		Blob:           content.Blob{Key: "bench/" + fileSHA, SHA256: fileSHA, Size: int64(128 + 4*space.Dimensions*n)},
	}
	return f, artifacts
}

// TestSaveSegmentationAndEmbeddingsStatementBounds owns the real-PostgreSQL
// statement-count gate (N=500) and byte-identical row round-trip for
// SaveSegmentation, SaveEmbeddingGroup, and insertCompactCoverage.
func TestSaveSegmentationAndEmbeddingsStatementBounds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tracer := &segmentWriteTracer{}
	pool := segmentWritePool(t, ctx, 4, tracer)

	const (
		org      = "stmt-bounds-org"
		corpusID = "stmt-bounds-corpus"
		recordID = "stmt-bounds-record"
		verID    = "stmt-bounds-version"
		n        = 500
	)
	seedSegmentWriteVersion(t, ctx, pool, org, corpusID, recordID, verID)

	proj := ProjectionStore{Pool: pool}
	emb := EmbeddingStore{Pool: pool}

	seg := buildTestSegmentation(org, verID, "plugin:bench@1", n)
	tracer.Reset()
	if err := proj.SaveSegmentation(ctx, org, seg); err != nil {
		t.Fatal(err)
	}
	segDataStmts, segTotalStmts, segTrips := tracer.Snapshot()
	t.Logf("SaveSegmentation(N=%d): data_stmts=%d total_stmts=%d roundtrips=%d", n, segDataStmts, segTotalStmts, segTrips)
	if segDataStmts > 3 {
		t.Fatalf("SaveSegmentation(N=%d) executed %d data statements, want <= 3", n, segDataStmts)
	}

	// Verify byte-identical segment rows in ordinal order.
	stored, err := proj.StoredSegmentation(ctx, org, verID, seg.Recipe)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ID != seg.ID || stored.Digest != content.SegmentationDigest(seg) || len(stored.Segments) != n {
		t.Fatalf("StoredSegmentation mismatch: id=%s len=%d", stored.ID, len(stored.Segments))
	}
	for i, want := range seg.Segments {
		got := stored.Segments[i]
		if got.ID != want.ID || got.PartKey != want.PartKey || got.Start != want.Start || got.End != want.End ||
			got.Derivation.Ordinal != want.Derivation.Ordinal || got.Derivation.ModelInputSHA256 != want.Derivation.ModelInputSHA256 {
			t.Fatalf("segment[%d] mismatch: got %+v, want %+v", i, got, want)
		}
	}
	var hashMatches int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM segments WHERE organization=$1 AND segmentation_id=$2`, org, seg.ID).Scan(&hashMatches); err != nil || hashMatches != n {
		t.Fatalf("stored segment row count = %d, err=%v, want %d", hashMatches, err, n)
	}

	// Idempotent replay and digest conflict check.
	tracer.Reset()
	if err = proj.SaveSegmentation(ctx, org, seg); err != nil {
		t.Fatalf("replay SaveSegmentation failed: %v", err)
	}
	replayData, _, _ := tracer.Snapshot()
	if replayData != 1 {
		t.Fatalf("replay SaveSegmentation executed %d data statements, want 1", replayData)
	}
	divergent := seg
	divergent.Segments = append([]content.Segment(nil), seg.Segments...)
	divergent.Segments[0].Text = "mutated-text"
	if err = proj.SaveSegmentation(ctx, org, divergent); !errors.Is(err, content.ErrConflict) {
		t.Fatalf("divergent SaveSegmentation err = %v, want ErrConflict", err)
	}

	// SaveEmbeddingGroup at N=500.
	space := content.VectorSpace{ID: "bench-space", Dimensions: 2, Manifest: json.RawMessage(`{"dimensions":2}`)}
	file, artifacts := buildTestEmbeddings(org, corpusID, seg, space, "plugin:bench@1")
	tracer.Reset()
	saved, err := emb.SaveEmbeddingGroup(ctx, file, space, artifacts, "")
	if err != nil || !saved {
		t.Fatalf("SaveEmbeddingGroup saved=%v err=%v", saved, err)
	}
	embDataStmts, embTotalStmts, embTrips := tracer.Snapshot()
	t.Logf("SaveEmbeddingGroup(N=%d): data_stmts=%d total_stmts=%d roundtrips=%d", n, embDataStmts, embTotalStmts, embTrips)
	if embDataStmts > 4 {
		t.Fatalf("SaveEmbeddingGroup(N=%d) executed %d data statements, want <= 4", n, embDataStmts)
	}

	gotFile, gotArtifacts, err := emb.EmbeddingGroup(ctx, org, seg.ID, space.ID)
	if err != nil || len(gotArtifacts) != n || gotFile.RowCount != n || gotFile.Blob.SHA256 != file.Blob.SHA256 {
		t.Fatalf("EmbeddingGroup len=%d file=%+v err=%v", len(gotArtifacts), gotFile, err)
	}
	for i, want := range artifacts {
		got := gotArtifacts[i]
		if got.ID != want.ID || got.SegmentID != want.SegmentID || got.Ordinal != want.Ordinal ||
			got.Payload.SHA256 != want.Payload.SHA256 || got.DerivationID != want.DerivationID ||
			got.InputSHA != want.InputSHA || got.SourceSHA != want.SourceSHA {
			t.Fatalf("embedding[%d] mismatch: got %+v, want %+v", i, got, want)
		}
	}

	// Expected-digest mismatch returns saved=false; producer conflict returns ErrConflict.
	if saved, err = emb.SaveEmbeddingGroup(ctx, file, space, artifacts, "wrong-expected"); err != nil || saved {
		t.Fatalf("stale expected digest: saved=%v err=%v, want false, nil", saved, err)
	}
	conflictFile := file
	conflictFile.Producer = "other-producer"
	if _, err = emb.SaveEmbeddingGroup(ctx, conflictFile, space, artifacts, file.Blob.SHA256); !errors.Is(err, content.ErrConflict) {
		t.Fatalf("producer conflict err = %v, want ErrConflict", err)
	}

	// insertCompactCoverage at N=500.
	g := content.Generation{ID: "gen-bounds", SpaceID: space.ID, Spaces: []content.GenerationSpace{{ID: space.ID}}, SpacesProjected: true}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tracer.Reset()
	inserted, err := insertCompactCoverage(ctx, tx, org, seg, g, gotArtifacts)
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	covDataStmts, covTotalStmts, covTrips := tracer.Snapshot()
	t.Logf("insertCompactCoverage(N=%d): data_stmts=%d total_stmts=%d roundtrips=%d", n, covDataStmts, covTotalStmts, covTrips)
	if inserted != n {
		t.Fatalf("insertCompactCoverage inserted = %d, want %d", inserted, n)
	}
	if covDataStmts > 4 {
		t.Fatalf("insertCompactCoverage(N=%d) executed %d data statements, want <= 4", n, covDataStmts)
	}

	// Replay coverage returns 0 new bits; missing row returns pgx.ErrNoRows;
	// tampered artifact returns content.ErrConflict.
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := insertCompactCoverage(ctx, tx, org, seg, g, gotArtifacts)
	if err != nil || replayed != 0 {
		_ = tx.Rollback(ctx)
		t.Fatalf("replay coverage = %d, %v; want 0, nil", replayed, err)
	}
	tampered := append([]content.Embedding(nil), gotArtifacts[:2]...)
	tampered[1].ID = content.Hash([]byte("tampered"))
	if _, err = insertCompactCoverage(ctx, tx, org, seg, g, tampered); !errors.Is(err, content.ErrConflict) {
		_ = tx.Rollback(ctx)
		t.Fatalf("tampered coverage err = %v, want ErrConflict", err)
	}
	missing := append([]content.Embedding(nil), gotArtifacts[:2]...)
	missing[1].SegmentID = "absent-segment"
	if _, err = insertCompactCoverage(ctx, tx, org, seg, g, missing); !errors.Is(err, pgx.ErrNoRows) {
		_ = tx.Rollback(ctx)
		t.Fatalf("missing coverage err = %v, want ErrNoRows", err)
	}
	_ = tx.Rollback(ctx)
}

// TestSortedSegmentUnnestDeadlockFreeAndReplayConverges proves:
//  1. 32 goroutines x 20 rounds writing overlapping segment sets in shuffled
//     order produce 0 SQLSTATE 40P01 deadlocks across SaveEmbeddingGroup,
//     SaveSegmentation, and multi-file insertCompactCoverage.
//  2. Mid-batch failure interruption followed by replay converges cleanly with
//     byte-identical rows via ON CONFLICT DO NOTHING.
func TestSortedSegmentUnnestDeadlockFreeAndReplayConverges(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := segmentWritePool(t, ctx, 32, nil)

	const (
		org       = "deadlock-stress-org"
		corpusID  = "deadlock-stress-corpus"
		workers   = 32
		rounds    = 20
		batchSize = 16
	)
	for w := range workers {
		seedSegmentWriteVersion(t, ctx, pool, org, corpusID, fmt.Sprintf("rec-%02d", w), fmt.Sprintf("ver-%02d", w))
	}

	emb := EmbeddingStore{Pool: pool}
	proj := ProjectionStore{Pool: pool}

	// Pre-register shared spaces so workers collide directly on storage_segments
	// and compact_embeddings PKs rather than space bootstrap.
	spaces := make([]content.VectorSpace, 4)
	for i := range spaces {
		spaces[i] = content.VectorSpace{ID: fmt.Sprintf("stress-space-%d", i), Dimensions: 2, Manifest: json.RawMessage(`{"dimensions":2}`)}
	}

	assertNoDeadlock := func(label string, errs []error) {
		t.Helper()
		for _, err := range errs {
			if err == nil {
				continue
			}
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "40P01" {
				t.Fatalf("%s encountered deadlock (SQLSTATE 40P01): %v", label, err)
			}
			t.Fatalf("%s unexpected error: %v", label, err)
		}
	}

	// 1. Overlapping SaveEmbeddingGroup: 32 goroutines x 20 rounds.
	for round := range rounds {
		start := make(chan struct{})
		var wg sync.WaitGroup
		errs := make([]error, workers)
		space := spaces[round%len(spaces)]
		for w := range workers {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				verID := fmt.Sprintf("ver-%02d", w)
				segID := fmt.Sprintf("emb-seg-%02d-%02d", w, round)
				presence := make([]byte, (batchSize+7)/8)
				artifacts := make([]content.Embedding, batchSize)
				for i := range batchSize {
					content.MarkPresent(presence, i)
					// Permute overlapping segment IDs differently per worker:
					// even workers ascend from rotated offset, odd workers descend.
					var idx int
					if w%2 == 0 {
						idx = (w + i) % (batchSize + 4)
					} else {
						idx = (batchSize + 4 - 1 - ((w + i) % (batchSize + 4)))
					}
					sharedSegID := fmt.Sprintf("shared-emb-r%02d-s%02d", round, idx)
					vsha := content.Hash([]byte(fmt.Sprintf("v:%d:%d:%d", round, w, i)))
					asha := content.Hash([]byte(fmt.Sprintf("a:%d:%d:%d", round, w, i)))
					artifacts[i] = content.Embedding{
						ID:             asha,
						Organization:   org,
						CorpusID:       corpusID,
						VersionID:      verID,
						SegmentationID: segID,
						SegmentID:      sharedSegID,
						SpaceID:        space.ID,
						Producer:       "stress",
						Ordinal:        i,
						Payload:        content.Blob{SHA256: vsha, Size: 8},
					}
				}
				fsha := content.Hash([]byte(fmt.Sprintf("f:%d:%d", round, w)))
				f := content.EmbeddingFile{
					Organization:   org,
					CorpusID:       corpusID,
					VersionID:      verID,
					SegmentationID: segID,
					Recipe:         "stress@1",
					SpaceID:        space.ID,
					Producer:       "stress",
					Dimensions:     2,
					RowCount:       batchSize,
					Presence:       presence,
					Blob:           content.Blob{Key: "stress/" + fsha, SHA256: fsha, Size: 256},
				}
				<-start
				saved, err := emb.SaveEmbeddingGroup(ctx, f, space, artifacts, "")
				if err != nil {
					errs[w] = err
					return
				}
				if !saved {
					errs[w] = fmt.Errorf("worker %d round %d: expected saved=true", w, round)
				}
			}(w)
		}
		close(start)
		wg.Wait()
		assertNoDeadlock(fmt.Sprintf("SaveEmbeddingGroup round %d", round), errs)
	}

	// 2. Overlapping SaveSegmentation: 32 goroutines x 20 rounds.
	for round := range rounds {
		start := make(chan struct{})
		var wg sync.WaitGroup
		errs := make([]error, workers)
		for w := range workers {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				verID := fmt.Sprintf("ver-%02d", w)
				recipe := fmt.Sprintf("stress-recipe-%02d", round)
				segID := content.StableID("segmentation", org, verID, recipe)
				segs := make([]content.Segment, batchSize)
				for i := range batchSize {
					var idx int
					if w%2 == 0 {
						idx = (w + i) % (batchSize + 4)
					} else {
						idx = (batchSize + 4 - 1 - ((w + i) % (batchSize + 4)))
					}
					segs[i] = content.Segment{
						ID:      fmt.Sprintf("shared-seg-r%02d-s%02d", round, idx),
						PartKey: "body",
						Start:   i * 4,
						End:     (i + 1) * 4,
						Text:    fmt.Sprintf("t-%d-%d", round, idx),
						Derivation: content.SegmentDerivation{
							Ordinal: i,
						},
					}
				}
				s := content.Segmentation{
					ID:         segID,
					VersionID:  verID,
					Recipe:     recipe,
					Segments:   segs,
					Provenance: json.RawMessage(`{"stress":true}`),
				}
				<-start
				errs[w] = proj.SaveSegmentation(ctx, org, s)
			}(w)
		}
		close(start)
		wg.Wait()
		assertNoDeadlock(fmt.Sprintf("SaveSegmentation round %d", round), errs)
	}

	// 3. Multi-file concurrent insertCompactCoverage: 16 goroutines x 10 rounds.
	covVer := "ver-cov"
	seedSegmentWriteVersion(t, ctx, pool, org, corpusID, "rec-cov", covVer)
	covSeg := buildTestSegmentation(org, covVer, "plugin:cov-stress@1", 16)
	if err := proj.SaveSegmentation(ctx, org, covSeg); err != nil {
		t.Fatal(err)
	}
	covSpaces := make([]content.GenerationSpace, len(spaces))
	bySpaceArtifacts := make([][]content.Embedding, len(spaces))
	for i, sp := range spaces {
		covSpaces[i] = content.GenerationSpace{ID: sp.ID}
		f, arts := buildTestEmbeddings(org, corpusID, covSeg, sp, "plugin:cov-stress@1")
		if saved, err := emb.SaveEmbeddingGroup(ctx, f, sp, arts, ""); err != nil || !saved {
			t.Fatalf("seed space %s: saved=%v err=%v", sp.ID, saved, err)
		}
		_, loaded, err := emb.EmbeddingGroup(ctx, org, covSeg.ID, sp.ID)
		if err != nil {
			t.Fatal(err)
		}
		bySpaceArtifacts[i] = loaded
	}
	covGen := content.Generation{ID: "gen-cov-stress", SpaceID: spaces[0].ID, Spaces: covSpaces, SpacesProjected: true}
	for round := range 10 {
		start := make(chan struct{})
		var wg sync.WaitGroup
		errs := make([]error, 16)
		for w := range 16 {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				// Mix all 4 files in worker-specific permuted order.
				var batch []content.Embedding
				for sIdx := range len(spaces) {
					sp := (sIdx + w) % len(spaces)
					if w%2 == 1 {
						sp = len(spaces) - 1 - sp
					}
					batch = append(batch, bySpaceArtifacts[sp][w])
					batch = append(batch, bySpaceArtifacts[sp][(w+round)%16])
				}
				<-start
				tx, err := pool.Begin(ctx)
				if err != nil {
					errs[w] = err
					return
				}
				if _, err = insertCompactCoverage(ctx, tx, org, covSeg, covGen, batch); err != nil {
					_ = tx.Rollback(ctx)
					errs[w] = err
					return
				}
				errs[w] = tx.Commit(ctx)
			}(w)
		}
		close(start)
		wg.Wait()
		assertNoDeadlock(fmt.Sprintf("insertCompactCoverage round %d", round), errs)
	}
	for i, arts := range bySpaceArtifacts {
		var covered []byte
		if err := pool.QueryRow(ctx, `SELECT covered FROM compact_embedding_coverage WHERE file_id=$1 AND generation_id=$2`, arts[0].File.ID, covGen.ID).Scan(&covered); err != nil {
			t.Fatalf("space %d coverage missing: %v", i, err)
		}
		for ord := range 16 {
			if !content.Present(covered, ord) {
				t.Fatalf("space %d missing bit %d in coverage %x", i, ord, covered)
			}
		}
	}

	// 4. Mid-batch kill + replay convergence on SaveSegmentation and SaveEmbeddingGroup.
	replayVer := "ver-replay"
	seedSegmentWriteVersion(t, ctx, pool, org, corpusID, "rec-replay", replayVer)
	replaySeg := buildTestSegmentation(org, replayVer, "plugin:replay@1", 500)

	if _, err := pool.Exec(ctx, `CREATE FUNCTION fail_mid_segments() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE n integer;
BEGIN
 SELECT count(*) INTO n FROM segments WHERE organization=NEW.organization AND segmentation_id=NEW.segmentation_id;
 IF n >= 250 THEN
  RAISE EXCEPTION 'synthetic mid-batch segment kill';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER fail_mid_segments BEFORE INSERT ON segments FOR EACH ROW EXECUTE FUNCTION fail_mid_segments()`); err != nil {
		t.Fatal(err)
	}
	if err := proj.SaveSegmentation(ctx, org, replaySeg); err == nil {
		t.Fatal("expected mid-batch segment trigger failure")
	}
	var leaked int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM segments WHERE organization=$1 AND segmentation_id=$2`, org, replaySeg.ID).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("aborted mid-batch SaveSegmentation leaked %d rows (err=%v)", leaked, err)
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER fail_mid_segments ON segments; DROP FUNCTION fail_mid_segments()`); err != nil {
		t.Fatal(err)
	}
	if err := proj.SaveSegmentation(ctx, org, replaySeg); err != nil {
		t.Fatalf("replay SaveSegmentation after kill failed: %v", err)
	}
	storedReplay, err := proj.StoredSegmentation(ctx, org, replayVer, replaySeg.Recipe)
	if err != nil || len(storedReplay.Segments) != 500 {
		t.Fatalf("replayed segmentation len=%d err=%v, want 500", len(storedReplay.Segments), err)
	}
	for i, want := range replaySeg.Segments {
		if storedReplay.Segments[i].ID != want.ID || storedReplay.Segments[i].Derivation.Ordinal != want.Derivation.Ordinal {
			t.Fatalf("replayed segment[%d] mismatch: got %+v, want %+v", i, storedReplay.Segments[i], want)
		}
	}

	replaySpace := content.VectorSpace{ID: "replay-space", Dimensions: 2, Manifest: json.RawMessage(`{"dimensions":2}`)}
	replayFile, replayArts := buildTestEmbeddings(org, corpusID, replaySeg, replaySpace, "plugin:replay@1")
	if _, err := pool.Exec(ctx, `CREATE FUNCTION fail_mid_compact_embeddings() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE n integer;
BEGIN
 SELECT count(*) INTO n FROM compact_embeddings WHERE file_id=NEW.file_id;
 IF n >= 250 THEN
  RAISE EXCEPTION 'synthetic mid-batch compact_embeddings kill';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER fail_mid_compact_embeddings BEFORE INSERT ON compact_embeddings FOR EACH ROW EXECUTE FUNCTION fail_mid_compact_embeddings()`); err != nil {
		t.Fatal(err)
	}
	if _, err := emb.SaveEmbeddingGroup(ctx, replayFile, replaySpace, replayArts, ""); err == nil {
		t.Fatal("expected mid-batch compact_embeddings trigger failure")
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER fail_mid_compact_embeddings ON compact_embeddings; DROP FUNCTION fail_mid_compact_embeddings()`); err != nil {
		t.Fatal(err)
	}

	// Partial group write (first 250) followed by full replay (all 500 in reversed order)
	// verifies ON CONFLICT DO NOTHING converges cleanly to byte-identical rows.
	partialFile := replayFile
	partialPresence := make([]byte, len(replayFile.Presence))
	for i := range 250 {
		content.MarkPresent(partialPresence, i)
	}
	partialFile.Presence = partialPresence
	partialFile.Blob.SHA256 = content.Hash([]byte("partial-file"))
	partialFile.Blob.Key = "bench/" + partialFile.Blob.SHA256
	if saved, err := emb.SaveEmbeddingGroup(ctx, partialFile, replaySpace, replayArts[:250], ""); err != nil || !saved {
		t.Fatalf("partial SaveEmbeddingGroup saved=%v err=%v", saved, err)
	}
	reversed := append([]content.Embedding(nil), replayArts...)
	slices.Reverse(reversed)
	if saved, err := emb.SaveEmbeddingGroup(ctx, replayFile, replaySpace, reversed, partialFile.Blob.SHA256); err != nil || !saved {
		t.Fatalf("full replay SaveEmbeddingGroup saved=%v err=%v", saved, err)
	}
	gotFile, gotArts, err := emb.EmbeddingGroup(ctx, org, replaySeg.ID, replaySpace.ID)
	if err != nil || len(gotArts) != 500 || !bytes.Equal(gotFile.Presence, replayFile.Presence) {
		t.Fatalf("converged EmbeddingGroup len=%d err=%v", len(gotArts), err)
	}
	for i, want := range replayArts {
		if gotArts[i].ID != want.ID || gotArts[i].SegmentID != want.SegmentID || gotArts[i].Payload.SHA256 != want.Payload.SHA256 {
			t.Fatalf("converged embedding[%d] mismatch: got %+v, want %+v", i, gotArts[i], want)
		}
	}
}

func BenchmarkSaveSegmentation(b *testing.B) {
	ctx := context.Background()
	tracer := &segmentWriteTracer{}
	pool := segmentWritePool(b, ctx, 4, tracer)
	const (
		org      = "bench-seg-org"
		corpusID = "bench-seg-corpus"
		n        = 500
	)
	proj := ProjectionStore{Pool: pool}
	segs := make([]content.Segmentation, b.N)
	for i := range b.N {
		verID := "bench-seg-ver-" + strconv.Itoa(i)
		seedSegmentWriteVersion(b, ctx, pool, org, corpusID, "bench-seg-rec-"+strconv.Itoa(i), verID)
		segs[i] = buildTestSegmentation(org, verID, "plugin:bench@1", n)
	}
	tracer.Reset()
	b.ResetTimer()
	for i := range b.N {
		if err := proj.SaveSegmentation(ctx, org, segs[i]); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	dataStmts, totalStmts, trips := tracer.Snapshot()
	b.ReportMetric(float64(dataStmts)/float64(b.N), "stmts/op")
	b.ReportMetric(float64(totalStmts)/float64(b.N), "total-stmts/op")
	b.ReportMetric(float64(trips)/float64(b.N), "roundtrips/op")
}

func BenchmarkSaveEmbeddings(b *testing.B) {
	ctx := context.Background()
	tracer := &segmentWriteTracer{}
	pool := segmentWritePool(b, ctx, 4, tracer)
	const (
		org      = "bench-emb-org"
		corpusID = "bench-emb-corpus"
		n        = 500
	)
	proj := ProjectionStore{Pool: pool}
	emb := EmbeddingStore{Pool: pool}
	space := content.VectorSpace{ID: "bench-emb-space", Dimensions: 2, Manifest: json.RawMessage(`{"dimensions":2}`)}

	files := make([]content.EmbeddingFile, b.N)
	arts := make([][]content.Embedding, b.N)
	for i := range b.N {
		verID := "bench-emb-ver-" + strconv.Itoa(i)
		seedSegmentWriteVersion(b, ctx, pool, org, corpusID, "bench-emb-rec-"+strconv.Itoa(i), verID)
		seg := buildTestSegmentation(org, verID, "plugin:bench@1", n)
		if err := proj.SaveSegmentation(ctx, org, seg); err != nil {
			b.Fatal(err)
		}
		files[i], arts[i] = buildTestEmbeddings(org, corpusID, seg, space, "plugin:bench@1")
	}
	tracer.Reset()
	b.ResetTimer()
	for i := range b.N {
		saved, err := emb.SaveEmbeddingGroup(ctx, files[i], space, arts[i], "")
		if err != nil || !saved {
			b.Fatalf("SaveEmbeddingGroup iter %d: saved=%v err=%v", i, saved, err)
		}
	}
	b.StopTimer()
	dataStmts, totalStmts, trips := tracer.Snapshot()
	b.ReportMetric(float64(dataStmts)/float64(b.N), "stmts/op")
	b.ReportMetric(float64(totalStmts)/float64(b.N), "total-stmts/op")
	b.ReportMetric(float64(trips)/float64(b.N), "roundtrips/op")
}
