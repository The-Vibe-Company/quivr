package postgres_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/app"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
)

// Conversion can stop after upload, resume, and retire only explicitly selected
// historical detail while preserving canonical vectors and coverage subsets.
func TestStorageCompactionResumesAcrossActivation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := scratchDatabase(t, ctx)
	if err := app.BootstrapDatabase(ctx, pool, app.DeploymentSpaces(nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE storage_state SET compact=false`); err != nil {
		t.Fatal(err)
	}
	org := "compaction-owner"
	scope := corpus.Scope{Organization: org, Actions: []string{"corpora:write", "content:write", "content:read"}, Corpora: []string{"*"}}
	c, _, err := (corpus.Service{Store: postgres.Store{Pool: pool}}).Create(ctx, scope, corpus.CreateInput{Key: "files", Name: "Files"})
	if err != nil {
		t.Fatal(err)
	}
	objects := &objectMemory{objects: map[string][]byte{}}
	stores := contentStores(pool)
	service := content.Service{Submissions: stores, Materialization: stores, Versions: stores, RecordStore: stores, Receipts: stores, Baseline: stores, Embeddings: stores, Blobs: objects}
	cmd := content.Command{Key: "same-request", Source: content.Source{CorpusID: c.ID, Namespace: "example", RecordKey: "record"}, Content: content.Text{Kind: "text", Text: "First second third"}}
	receipt, err := service.Accept(ctx, scope, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Materialize(ctx, org, receipt.ID); err != nil {
		t.Fatal(err)
	}
	v, err := service.ProcessingVersion(ctx, org, receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	seg, err := content.PluginSegmentation(org, v, "plugin:core.ingest@1.0.0", json.RawMessage(`{}`), []content.SegmentInput{{PartKey: "body", Start: 0, End: 5}, {PartKey: "body", Start: 6, End: 12}, {PartKey: "body", Start: 13, End: 18}})
	if err != nil {
		t.Fatal(err)
	}
	if err = service.SaveSegmentation(ctx, org, v, seg); err != nil {
		t.Fatal(err)
	}
	g, err := stores.Generation(ctx, org, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	space := content.VectorSpace{ID: g.SpaceID, Dimensions: 2}
	if err = pool.QueryRow(ctx, `SELECT manifest FROM vector_spaces WHERE id=$1`, space.ID).Scan(&space.Manifest); err != nil {
		t.Fatal(err)
	}
	artifact, err := service.SaveEmbedding(ctx, content.EmbeddingInput(org, c.ID, v, seg, seg.Segments[1], space, "plugin:core.ingest@1.0.0"), space, []float32{3, 4})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.SaveEmbedding(ctx, content.EmbeddingInput(org, c.ID, v, seg, seg.Segments[2], space, "plugin:core.ingest@1.0.0"), space, []float32{5, 6}); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO embedding_coverage(organization,segment_id,generation_id,artifact_id,space_id) VALUES($1,$2,$3,$4,$5)`, org, artifact.SegmentID, g.ID, artifact.ID, space.ID); err != nil {
		t.Fatal(err)
	}
	normalization := content.Normalized{Manifest: content.Blob{Key: "manifest", SHA256: "manifest-digest", Size: 1}, Provenance: content.Normalization{PluginID: "example.normalize", PluginVersion: "1.0.0", InvocationID: "call", IdempotencyKey: "ledger"}, Extensions: content.Extensions{"example.normalize.metadata": {SchemaVersion: "1", Data: map[string]any{"heading": "Preserved"}}}}
	ledger := postgres.NormalizationStore{Pool: pool, Blobs: objects, RetainImportAuditDetail: true}
	if _, err = ledger.SaveNormalized(ctx, org, v.ID, normalization); err != nil {
		t.Fatal(err)
	}
	pendingCommand := cmd
	pendingCommand.Key = "pending-request"
	pendingCommand.Source.RecordKey = "pending"
	pendingCommand.Revision = "revision-1"
	pendingCommand.Position = "1"
	pendingReceipt, err := service.Accept(ctx, scope, pendingCommand)
	if err != nil {
		t.Fatal(err)
	}
	pendingWork, _, err := (postgres.MaterializationStore{Pool: pool}).Work(ctx, org, pendingReceipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	originalObjects := len(objects.objects)
	runner := postgres.StorageCompaction{Pool: pool, Blobs: objects}
	if _, err = runner.Start(ctx, "expand", false); err != nil {
		t.Fatal(err)
	}
	first, err := runner.Step(ctx, "expand")
	if err != nil {
		t.Fatal(err)
	}
	if first.GroupsDone != 1 || first.Checkpoint == "" {
		t.Fatalf("missing resume checkpoint %+v", first)
	}
	var legacy int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM embedding_artifacts`).Scan(&legacy); err != nil || legacy != 2 {
		t.Fatalf("compatibility artifact lost count=%d err=%v", legacy, err)
	}
	resumed, err := runner.Step(ctx, "expand")
	if err != nil || resumed.GroupsDone != 1 || resumed.Phase != "receipts" {
		t.Fatalf("retained group packed repeatedly: %+v %v", resumed, err)
	}
	if err = postgres.ActivateCompactStorage(ctx, pool); err != nil {
		t.Fatal(err)
	}
	finish := func(id string, retire bool) {
		t.Helper()
		status, err := runner.Start(ctx, id, retire)
		if err != nil {
			t.Fatal(err)
		}
		for n := 0; n < 20 && status.Phase != "done"; n++ {
			status, err = runner.Step(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
		}
		if status.Phase != "done" {
			t.Fatalf("did not finish %+v", status)
		}
	}
	finish("retire-vectors", false)
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM embedding_artifacts`).Scan(&legacy); err != nil || legacy != 0 {
		t.Fatalf("legacy vectors remain count=%d err=%v", legacy, err)
	}
	file, packed, err := (postgres.EmbeddingStore{Pool: pool}).EmbeddingGroup(ctx, org, seg.ID, space.ID)
	if err != nil || len(packed) != 2 {
		t.Fatalf("partial file %+v %v", packed, err)
	}
	var mask []byte
	if err = pool.QueryRow(ctx, `SELECT covered FROM compact_embedding_coverage WHERE file_id=$1 AND generation_id=$2`, file.ID, g.ID).Scan(&mask); err != nil {
		t.Fatal(err)
	}
	if content.Present(mask, 0) || !content.Present(mask, 1) || content.Present(mask, 2) {
		t.Fatalf("partial coverage broadened %v", mask)
	}
	n, found, err := ledger.Normalized(ctx, org, v.ID)
	if err != nil || !found || n.Provenance.InvocationID != "call" {
		t.Fatalf("historical audit retired without selection %+v %v", n, err)
	}
	finish("retire-detail", true)
	finish("retire-detail", true)
	n, found, err = ledger.Normalized(ctx, org, v.ID)
	if err != nil || !found || n.Provenance.InvocationID != "" || n.Extensions["example.normalize.metadata"].Data["heading"] != "Preserved" {
		t.Fatalf("required outcome lost %+v %v", n, err)
	}
	var canonical []byte
	var digest []byte
	if err = pool.QueryRow(ctx, `SELECT canonical_request,request_digest FROM ingestion_receipts WHERE organization=$1 AND id=$2`, org, receipt.ID).Scan(&canonical, &digest); err != nil || len(canonical) != 0 || len(digest) != 32 {
		t.Fatalf("receipt conversion bytes=%d digest=%d %v", len(canonical), len(digest), err)
	}
	var pendingInput []byte
	if err = pool.QueryRow(ctx, `SELECT command FROM accepted_revisions WHERE organization=$1 AND record_id=$2 AND version_id=$3`, org, pendingWork.RecordID, pendingWork.VersionID).Scan(&pendingInput); err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(pendingInput, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields["idempotency_key"]) != 0 || len(fields["source_revision"]) != 0 || len(fields["source_position"]) != 0 {
		t.Fatalf("pending execution retained optional detail: %s", pendingInput)
	}
	if err = service.Materialize(ctx, org, pendingReceipt.ID); err != nil {
		t.Fatalf("pending required input lost: %v", err)
	}
	replay, err := service.Accept(ctx, scope, cmd)
	if err != nil || replay.ID != receipt.ID {
		t.Fatalf("digest replay %v %+v", err, replay)
	}
	restored, vector, err := service.LoadEmbedding(ctx, org, artifact.DerivationID)
	if err != nil || restored.ID != artifact.ID || len(vector) != 2 || vector[0] != 3 || vector[1] != 4 {
		t.Fatalf("canonical vector changed %v %+v %v", err, restored, vector)
	}
	if len(objects.objects) < originalObjects {
		t.Fatal("physical objects were deleted")
	}
}
