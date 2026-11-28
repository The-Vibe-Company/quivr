package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
)

// UpdateSegmentLexicalText persists Phase-2 lexical enrichment in segments.derivation
// and updates segmentations.digest atomically so PluginSegmentationOf verifies
// cleanly, Segment.ID and text_sha256 remain unchanged, retries are idempotent,
// and stale digests fail with ErrConflict.
func TestUpdateSegmentLexicalTextPersistsCanonicalDerivationAndDigest(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := rebuildAdapterPool(t, ctx)
	store := contentStores(pool)

	org := fmt.Sprintf("adapter-lexical-%d", time.Now().UnixNano())
	scope := corpus.Scope{Organization: org, Actions: []string{"corpora:write", "content:write", "content:read", "search:query"}, Corpora: []string{"*"}}
	c, _, err := (corpus.Service{Store: postgres.Store{Pool: pool}}).Create(ctx, scope, corpus.CreateInput{Key: "lexical", Name: "Lexical"})
	if err != nil {
		t.Fatal(err)
	}
	service := content.Service{Submissions: store, Receipts: store, RecordStore: store, Versions: store, Materialization: store, Baseline: store}
	const text = "Harbour strike. Port closed."
	r, err := service.Accept(ctx, scope, content.Command{
		Key:     "article",
		Source:  content.Source{CorpusID: c.ID, Namespace: "lexical", RecordKey: "article"},
		Content: content.Text{Kind: "text", Text: text},
	})
	if err != nil {
		t.Fatal(err)
	}
	work, _, err := store.Work(ctx, org, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Publish(ctx, work, publication(
		content.Blob{Key: "fixture/text", SHA256: "lexical-text", Size: int64(len(text))},
		content.Blob{Key: "fixture/manifest", SHA256: "lexical-manifest", Size: 2},
	)); err != nil {
		t.Fatal(err)
	}
	v := content.Version{
		ID:       work.VersionID,
		RecordID: work.RecordID,
		Manifest: content.Manifest{Parts: []content.Part{{Key: "body", Role: "body", Content: content.Text{Kind: "text", Text: text}}}},
	}
	const recipe = "plugin:example.lexical@1.0.0"
	seg, err := content.PluginSegmentation(org, v, recipe, json.RawMessage(`{"plugin_id":"example.lexical"}`), []content.SegmentInput{
		{PartKey: "body", Start: 0, End: 15, LexicalText: "harbour strike"},
		{PartKey: "body", Start: 16, End: 28},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SaveSegmentation(ctx, org, seg); err != nil {
		t.Fatal(err)
	}

	var beforeSHA string
	if err = pool.QueryRow(ctx, `SELECT text_sha256 FROM segments WHERE organization=$1 AND id=$2`, org, seg.Segments[0].ID).Scan(&beforeSHA); err != nil {
		t.Fatal(err)
	}

	updates := []content.SegmentLexicalUpdate{
		{SegmentID: seg.Segments[0].ID, LexicalText: "harbour strike\nport dock stoppage"},
	}
	updated, err := service.UpdateSegmentLexicalText(ctx, org, seg, updates)
	if err != nil {
		t.Fatalf("UpdateSegmentLexicalText: %v", err)
	}

	// Canonical read-back verifies both updated LexicalText and updated segmentations.digest.
	stored, err := service.PluginSegmentationOf(ctx, org, v, recipe)
	if err != nil {
		t.Fatalf("PluginSegmentationOf after lexical update: %v", err)
	}
	if content.SegmentationDigest(stored) != content.SegmentationDigest(updated) {
		t.Fatalf("digest mismatch after read-back: %s vs %s", content.SegmentationDigest(stored), content.SegmentationDigest(updated))
	}
	if stored.Segments[0].Derivation.LexicalText != "harbour strike\nport dock stoppage" {
		t.Fatalf("stored LexicalText = %q", stored.Segments[0].Derivation.LexicalText)
	}
	if stored.Segments[0].ID != seg.Segments[0].ID || stored.Segments[0].Text != seg.Segments[0].Text {
		t.Fatalf("segment identity or text changed: %+v", stored.Segments[0])
	}
	var afterSHA string
	if err = pool.QueryRow(ctx, `SELECT text_sha256 FROM segments WHERE organization=$1 AND id=$2`, org, seg.Segments[0].ID).Scan(&afterSHA); err != nil {
		t.Fatal(err)
	}
	if afterSHA != beforeSHA {
		t.Fatalf("segments.text_sha256 mutated: %s -> %s", beforeSHA, afterSHA)
	}

	// Idempotent retry with original seg and same updates succeeds cleanly.
	if retry, err := store.UpdateSegmentLexicalText(ctx, org, seg, updates); err != nil || content.SegmentationDigest(retry) != content.SegmentationDigest(updated) {
		t.Fatalf("idempotent retry failed: err=%v seg=%+v", err, retry)
	}

	// Stale base trying to apply a conflicting update fails with ErrConflict.
	if _, err := store.UpdateSegmentLexicalText(ctx, org, seg, []content.SegmentLexicalUpdate{
		{SegmentID: seg.Segments[0].ID, LexicalText: "harbour strike\ndifferent"},
	}); !errors.Is(err, content.ErrConflict) {
		t.Fatalf("stale conflicting update err = %v, want ErrConflict", err)
	}
}
