package weaviate_test

import (
	"context"
	_ "embed"
	"encoding/json"
	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/tei"
	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/tokenizer"
	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/weaviate"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/processing"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

//go:embed testdata/relevance-v1.json
var fixture []byte

// This measures the accepted explicit title/body recipe at the real adapter seam.
// Public tests separately verify authorization, canonical hydration and durability;
// structured Manifest ingestion belongs to THE-648.
func TestFR_ENRelevanceByMode(t *testing.T) {
	path := os.Getenv("QUIVR_ADAPTER_CONFIG")
	if path == "" {
		t.Skip("make verify real adapters")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		TEIURL      string           `json:"tei_url"`
		WeaviateURL string           `json:"weaviate_url"`
		Tokenizer   tokenizer.Config `json:"tokenizer"`
	}
	if err = json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	// The complete CPU fixture includes ingestion, 24 query embeddings and 72 real
	// searches. Shared CI runners are slow; no individual request should be stuck.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	model := tei.Encoder{Endpoint: cfg.TEIURL}
	// The persistent tokenizer is the one production runs; TestServerMatchesPinnedReference
	// holds it to the one-shot reference, which costs a process start per call.
	server := &tokenizer.Server{Config: cfg.Tokenizer}
	defer server.Close()
	windows := processing.TokenWindows{Tokenizer: server}
	projection := weaviate.New(cfg.WeaviateURL)
	g := content.Generation{ID: "relevance-fixture", Collection: "QuivrRelevanceV1", ProfileVersion: retrieval.ProfileVersion, SpaceID: model.Space().ID}
	if err = projection.Bootstrap(ctx, g.Collection); err != nil {
		t.Fatal(err)
	}
	scope := corpus.Scope{Organization: "relevance-fixture", Corpora: []string{"fixture"}}
	var rows [][]string
	if err = json.Unmarshal(fixture, &rows); err != nil {
		t.Fatal(err)
	}
	expected := map[string]string{}
	for _, r := range rows {
		v := content.Version{ID: r[0], RecordID: r[0], Manifest: content.Manifest{Kind: "text", Parts: []content.Part{{Key: "title", Role: "title", Content: content.Text{Kind: "text", Text: r[1]}}, {Key: "body", Role: "body", Content: content.Text{Kind: "text", Text: r[2]}}}}}
		seg, err := windows.Process(ctx, processing.Input{Organization: scope.Organization, Version: v})
		if err != nil || len(seg.Segments) != 1 {
			t.Fatal("fixture segmentation", err)
		}
		p := seg.Segments[0]
		expected[p.ID] = r[0]
		vector, err := model.Embed(ctx, p.Derivation.ModelInput)
		if err != nil {
			t.Fatal(err)
		}
		if err = projection.Publish(ctx, g, scope.Organization, "fixture", v, seg); err != nil {
			t.Fatal(err)
		}
		raw, _ := content.VectorBytes(vector)
		e := content.Embedding{Organization: scope.Organization, SegmentID: p.ID, SpaceID: g.SpaceID, Payload: content.Blob{SHA256: content.Hash(raw)}}
		if err = projection.PublishEmbeddings(ctx, g, scope.Organization, []content.EmbeddingData{{Artifact: e, Vector: vector}}); err != nil {
			t.Fatal(err)
		}
	}
	report := map[string]any{"fixture": "original CC0 FR/EN v1; 24 queries, one relevant document each", "fixture_sha256": content.Hash(fixture), "scope": "real processing/TEI/Weaviate adapters with explicit title/body in a fixture-only collection (BM25 statistics isolated); public ingestion remains inline body until THE-648", "profile": retrieval.ProfileVersion, "space_id": model.Space().ID, "producer": model.Producer(), "known_limitation": "THE-641: prior semantic MRR@10 .9583/Recall@3 1; hybrid .7969/.9583. Report separately, never tune to fixture."}
	// Each query is normalized and embedded once; the three modes search with the same inputs.
	queries := make([]string, len(rows))
	vectors := make([][]float32, len(rows))
	for i, r := range rows {
		if queries[i], err = windows.NormalizeQuery(ctx, r[4]); err != nil {
			t.Fatal(err)
		}
		if vectors[i], err = model.Embed(ctx, "query: "+queries[i]); err != nil {
			t.Fatal(err)
		}
	}
	for _, mode := range []string{"lexical", "semantic", "hybrid"} {
		mrr, recall, ndcg := 0.0, 0.0, 0.0
		details := []any{}
		for i, r := range rows {
			q := retrieval.Request{Query: queries[i], Mode: mode, Profile: "balanced", Limit: 10, CorpusIDs: []string{"fixture"}}
			if mode != "lexical" {
				q.Vector = vectors[i]
			}
			hits, err := projection.Search(ctx, []retrieval.Route{{CorpusID: "fixture", Generation: g}}, scope, q)
			if err != nil {
				t.Fatal(err)
			}
			rank := 0
			ids := []string{}
			for i, h := range hits {
				if i == 10 {
					break
				}
				ids = append(ids, expected[h.SegmentID])
				if expected[h.SegmentID] == r[0] {
					rank = i + 1
				}
			}
			if rank > 0 {
				mrr += 1 / float64(rank)
				ndcg += 1 / math.Log2(float64(rank+1))
				if rank <= 3 {
					recall++
				}
			}
			details = append(details, map[string]any{"query_id": r[0], "rank": rank, "top10": ids})
		}
		report[mode] = map[string]any{"mrr_at_10": mrr / 24, "recall_at_3": recall / 24, "ndcg_at_10": ndcg / 24, "queries": details}
		t.Logf("%s MRR@10 %.4f Recall@3 %.4f nDCG@10 %.4f", mode, mrr/24, recall/24, ndcg/24)
		if mode == "semantic" && (mrr/24 < .90 || recall != 24) {
			t.Error("semantic smoke baseline regressed")
		}
	}
	out, _ := json.MarshalIndent(report, "", "  ")
	if err = os.WriteFile(filepath.Join(filepath.Dir(path), "relevance-report.json"), out, 0600); err != nil {
		t.Fatal(err)
	}
}
