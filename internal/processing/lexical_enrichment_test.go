package processing_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/processing"
)

func TestGateByCosineSalienceFiltersHallucinationsAndOrdersDeterministically(t *testing.T) {
	segVec := []float32{1, 0, 0}
	candidates := []processing.ScoredLexicalCandidate{
		{Text: "off-topic recipe", Vector: []float32{0, 1, 0}},             // cos 0.0 -> rejected
		{Text: "medium match", Vector: []float32{0.6, 0.8, 0}},             // cos 0.6 -> kept
		{Text: "strong match", Vector: []float32{0.9, 0.4358899, 0}},       // cos ~0.9 -> kept first
		{Text: "STRONG   Match", Vector: []float32{0.9, 0.4358899, 0}},     // duplicate must not consume maxKeep
		{Text: "exact tie a", Vector: []float32{0.8, 0.6, 0}},              // cos 0.8
		{Text: "exact tie b", Vector: []float32{0.8, -0.6, 0}},             // cos 0.8 (keeps input order)
		{Text: "missing vector"},                                           // fail-closed -> rejected
		{Text: "wrong dimension", Vector: []float32{1, 0}},                 // rejected
		{Text: "nan vector", Vector: []float32{float32(math.NaN()), 0, 0}}, // rejected
		{Text: "zero vector", Vector: []float32{0, 0, 0}},                  // rejected
		{Text: "opposite", Vector: []float32{-1, 0, 0}},                    // cos -1 -> rejected
	}
	got := processing.GateByCosineSalience(segVec, candidates, 0.55, 3)
	want := []string{"strong match", "exact tie a", "exact tie b"}
	if !slices.Equal(got, want) {
		t.Fatalf("GateByCosineSalience = %v, want %v", got, want)
	}
	if empty := processing.GateByCosineSalience(nil, candidates, 0.55, 3); len(empty) != 0 {
		t.Fatalf("empty segment vector must fail closed, got %v", empty)
	}
	if _, ok := processing.CosineSimilarity([]float32{1, 0}, []float32{1, 0, 0}); ok {
		t.Fatal("mismatched dimensions must return ok=false")
	}
}

func TestSynonymLexicalEnricherMatchesWholeTokenBoundariesDeterministically(t *testing.T) {
	enricher, err := processing.NewSynonymLexicalEnricher(map[string][]string{
		"solar panel": {"photovoltaic array", "PV module"},
		"car":         {"automobile"},
	})
	if err != nil {
		t.Fatalf("NewSynonymLexicalEnricher: %v", err)
	}
	seg := content.Segmentation{
		ID:        "seg-1",
		VersionID: "ver-1",
		Segments: []content.Segment{
			{ID: "s-match", Text: "A solar panel cuts carbon emissions."},
			{ID: "s-none", Text: "Carbon levels fell over winter."},
		},
	}
	got, err := enricher.EnrichLexical(t.Context(), processing.LexicalEnrichmentRequest{Segmentation: seg})
	if err != nil {
		t.Fatalf("EnrichLexical: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("matched %d segments, want 1 (%+v)", len(got), got)
	}
	want := []string{"photovoltaic array", "PV module"}
	if !slices.Equal(got["s-match"].Terms, want) {
		t.Fatalf("terms = %v, want %v (must not match car inside carbon)", got["s-match"].Terms, want)
	}
	if _, err := processing.NewSynonymLexicalEnricher(map[string][]string{
		"solar-panel": {"a"},
		"solar panel": {"b"},
	}); err == nil {
		t.Fatal("normalized synonym key collision must be rejected")
	}

	// Synonyms match only immutable Segment.Text, so previously appended
	// Derivation.LexicalText terms never cascade into new rules on retry.
	chained, err := processing.NewSynonymLexicalEnricher(map[string][]string{
		"solar":        {"photovoltaic"},
		"photovoltaic": {"pv cell"},
	})
	if err != nil {
		t.Fatal(err)
	}
	retrySeg := content.Segmentation{
		ID: "seg-2", VersionID: "ver-2",
		Segments: []content.Segment{{
			ID:         "s-retry",
			Text:       "solar output",
			Derivation: content.SegmentDerivation{LexicalText: "solar output\nphotovoltaic"},
		}},
	}
	retryOut, err := chained.EnrichLexical(t.Context(), processing.LexicalEnrichmentRequest{Segmentation: retrySeg})
	if err != nil || !slices.Equal(retryOut["s-retry"].Terms, []string{"photovoltaic"}) {
		t.Fatalf("chained synonym retry = %+v, %v; want only [photovoltaic]", retryOut, err)
	}
}

func TestHTTPLexicalEnricherParsesObjectAndStringQueriesAndHandlesErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  processing.HTTPLexicalEnricherConfig
	}{
		{"bad_scheme", processing.HTTPLexicalEnricherConfig{Endpoint: "ftp://expand.example.test/v1"}},
		{"missing_hostname", processing.HTTPLexicalEnricherConfig{Endpoint: "https://:8443/v1"}},
		{"userinfo_rejected", processing.HTTPLexicalEnricherConfig{Endpoint: "https://user:pass@expand.example.test/v1"}},
		{"fragment_rejected", processing.HTTPLexicalEnricherConfig{Endpoint: "https://expand.example.test/v1#frag"}},
		{"over_cap_timeout", processing.HTTPLexicalEnricherConfig{Endpoint: "https://expand.example.test/v1", Timeout: processing.MaxLexicalEnrichmentTimeout + time.Second}},
		{"plaintext_key_non_loopback", processing.HTTPLexicalEnricherConfig{Endpoint: "http://expand.example.test/v1", APIKey: "secret-token"}},
	} {
		t.Run("validation/"+tc.name, func(t *testing.T) {
			if _, err := processing.NewHTTPLexicalEnricher(tc.cfg); err == nil {
				t.Fatalf("NewHTTPLexicalEnricher(%s) succeeded, want error", tc.name)
			}
		})
	}

	var gotAuth string
	status := http.StatusOK
	body := `{"segments":[{"segment_id":"s1","terms":["synonym one","Synonym   One"],"queries":[{"text":"precomputed query","vector":[1,0]}, "plain query", "PLAIN QUERY"]},{"segment_id":"unknown","terms":["ignored"]}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	enricher, err := processing.NewHTTPLexicalEnricher(processing.HTTPLexicalEnricherConfig{
		Endpoint:   srv.URL,
		APIKey:     "secret-token",
		Timeout:    2 * time.Second,
		MaxQueries: 4,
		Client:     srv.Client(),
	})
	if err != nil {
		t.Fatalf("NewHTTPLexicalEnricher: %v", err)
	}
	req := processing.LexicalEnrichmentRequest{
		Organization: "org",
		CorpusID:     "corpus",
		Version:      content.Version{ID: "ver", RecordID: "rec"},
		Segmentation: content.Segmentation{Segments: []content.Segment{{ID: "s1", PartKey: "body", Text: "text"}}},
	}
	got, err := enricher.EnrichLexical(t.Context(), req)
	if err != nil {
		t.Fatalf("EnrichLexical: %v", err)
	}
	if gotAuth != "Bearer secret-token" {
		t.Fatalf("Authorization = %q, want Bearer secret-token", gotAuth)
	}
	exp, ok := got["s1"]
	// Remote HTTP terms and queries both route through cosine gating without trusting remote vectors.
	if !ok || len(got) != 1 || len(exp.Terms) != 0 || len(exp.Queries) != 3 {
		t.Fatalf("unexpected expansion: %+v", got)
	}
	for i, wantText := range []string{"synonym one", "precomputed query", "plain query"} {
		if exp.Queries[i].Text != wantText || len(exp.Queries[i].Vector) != 0 {
			t.Fatalf("query[%d] = %+v, want text=%q with nil vector", i, exp.Queries[i], wantText)
		}
	}

	status = http.StatusServiceUnavailable
	if _, err := enricher.EnrichLexical(t.Context(), req); err == nil {
		t.Fatal("503 response must return a retryable error")
	}
}

type lexicalMemoryBaseline struct {
	content.BaselineRepository
	stored      map[string]content.Segmentation
	updateCalls int
}

func (m *lexicalMemoryBaseline) SaveSegmentation(_ context.Context, _ string, seg content.Segmentation) error {
	if existing, ok := m.stored[seg.Recipe]; ok {
		if content.SegmentationDigest(existing) != content.SegmentationDigest(seg) {
			return content.ErrConflict
		}
		return nil
	}
	m.stored[seg.Recipe] = seg
	return nil
}

func (m *lexicalMemoryBaseline) StoredSegmentation(_ context.Context, _, _, recipe string) (content.StoredSegmentation, error) {
	seg, ok := m.stored[recipe]
	if !ok {
		return content.StoredSegmentation{}, corpus.ErrNotFound
	}
	out := content.StoredSegmentation{ID: seg.ID, Digest: content.SegmentationDigest(seg), Provenance: seg.Provenance}
	for _, p := range seg.Segments {
		out.Segments = append(out.Segments, content.StoredSegment{
			ID: p.ID, PartKey: p.PartKey, Start: p.Start, End: p.End, Derivation: p.Derivation,
		})
	}
	return out, nil
}

func (m *lexicalMemoryBaseline) UpdateSegmentLexicalText(_ context.Context, _ string, seg content.Segmentation, updates []content.SegmentLexicalUpdate) (content.Segmentation, error) {
	m.updateCalls++
	updated, _, err := content.ApplySegmentLexicalUpdates(seg, updates)
	if err != nil {
		return seg, err
	}
	m.stored[seg.Recipe] = updated
	return updated, nil
}

type recordingLexicalIndexer struct {
	indexCalls      int
	lexicalCalls    int
	lastSeg         content.Segmentation
	lastData        []content.EmbeddingData
	lastLexical     []content.SegmentLexicalUpdate
	failNextLexical bool
}

func (r *recordingLexicalIndexer) IndexEmbeddings(_ context.Context, _ string, _ content.Version, seg content.Segmentation, data []content.EmbeddingData) error {
	r.indexCalls++
	r.lastSeg = seg
	r.lastData = data
	return nil
}

func (r *recordingLexicalIndexer) IndexEmbeddingsWithLexical(_ context.Context, _ string, _ content.Version, seg content.Segmentation, data []content.EmbeddingData, lexical []content.SegmentLexicalUpdate) error {
	r.lexicalCalls++
	r.lastSeg = seg
	r.lastData = data
	r.lastLexical = append([]content.SegmentLexicalUpdate(nil), lexical...)
	if r.failNextLexical {
		r.failNextLexical = false
		return errors.New("simulated Weaviate lexical update failure")
	}
	return nil
}

type staticQueryEncoder struct {
	vectors map[string][]float32
	err     error
	calls   int
}

func (e *staticQueryEncoder) EncodeQuery(_ context.Context, _, _, q string) ([]float32, error) {
	e.calls++
	if e.err != nil {
		return nil, e.err
	}
	if v, ok := e.vectors[q]; ok {
		return v, nil
	}
	return nil, content.ErrInvalid
}

type staticLexicalEnricher struct {
	calls int
	fn    func(processing.LexicalEnrichmentRequest) map[string]processing.SegmentLexicalExpansion
}

func (e *staticLexicalEnricher) EnrichLexical(_ context.Context, req processing.LexicalEnrichmentRequest) (map[string]processing.SegmentLexicalExpansion, error) {
	e.calls++
	return e.fn(req), nil
}

type oneVersionWithManifest struct {
	oneVersion
	m content.Manifest
}

func (s *oneVersionWithManifest) Read(_ context.Context, b content.Blob) ([]byte, error) {
	if b.Key == "manifest" {
		return json.Marshal(s.m)
	}
	return []byte(content.NormalizedText(s.m)), nil
}

// Phase-2 lexical enrichment runs only in Stage-2 Enrich (never in Stage-1
// retrieval_ready), appends synonyms + cosine-gated synthetic queries to
// Segment.LexicalText without touching Segment.Text or dense vectors, and
// converges idempotently across retries after a projection outage.
func TestEnrichPhase2LexicalEnrichmentGatesPersistsAndRetriesIdempotently(t *testing.T) {
	m := content.Manifest{Kind: "manifest", Parts: []content.Part{{Key: "body", Role: "body", Content: content.Text{Kind: "text", Text: "a long article"}}}}
	store := &oneVersionWithManifest{m: m}
	baseline := &lexicalMemoryBaseline{stored: map[string]content.Segmentation{}}
	artifacts := &memoryArtifacts{byDerivation: map[string]content.Embedding{}, blobs: map[string][]byte{}}
	calls := 0
	// fillPlugin emits vector {1, 0} for space "p.large@1".
	p := fillPlugin{segments: []content.SegmentInput{{PartKey: "body", End: 14, LexicalText: "long article"}}, calls: &calls}
	indexer := &recordingLexicalIndexer{failNextLexical: true}

	syn, err := processing.NewSynonymLexicalEnricher(map[string][]string{"article": {"editorial piece"}})
	if err != nil {
		t.Fatal(err)
	}
	doc2query := &staticLexicalEnricher{fn: func(req processing.LexicalEnrichmentRequest) map[string]processing.SegmentLexicalExpansion {
		id := req.Segmentation.Segments[0].ID
		return map[string]processing.SegmentLexicalExpansion{
			id: {
				Queries: []processing.ScoredLexicalCandidate{
					{Text: "in-depth report"},         // encoded to {0.95, 0.31} -> passes cosine >= 0.6
					{Text: "unrelated pastry recipe"}, // encoded to {0, 1} -> rejected by cosine gate
				},
			},
		}
	}}
	encoder := &staticQueryEncoder{vectors: map[string][]float32{
		"in-depth report":         {0.95, 0.31},
		"unrelated pastry recipe": {0, 1},
	}}
	minCos := 0.6

	contents := content.Service{Receipts: store, RecordStore: store, Versions: store, Blobs: store, Baseline: baseline, Embeddings: store}
	deriverContents := content.Service{Baseline: baseline, Embeddings: artifacts, Blobs: artifacts}
	service := processing.Service{
		Content:         contents,
		Plugin:          &processing.PluginDeriver{Plugin: p, Content: deriverContents},
		Routing:         generationRoute{generation: content.Generation{ID: "generation", SpaceID: "p.large@1"}},
		Enrichment:      indexer,
		LexicalEnricher: processing.ChainLexicalEnricher{Enrichers: []processing.LexicalEnricher{syn, doc2query}},
		LexicalOptions:  processing.LexicalEnrichmentOptions{MinCosineSalience: &minCos, QueryEncoder: encoder},
	}

	// Attempt 1: Postgres persists enriched LexicalText, Weaviate fails -> retryable error.
	if err := service.Enrich(t.Context(), "org", "receipt"); err == nil {
		t.Fatal("expected retryable error on simulated Weaviate failure")
	}
	if baseline.updateCalls != 1 || indexer.lexicalCalls != 1 || indexer.indexCalls != 0 {
		t.Fatalf("attempt 1: pgUpdates=%d lexicalCalls=%d indexCalls=%d", baseline.updateCalls, indexer.lexicalCalls, indexer.indexCalls)
	}

	// Attempt 2 (retry): Postgres already has enriched LexicalText (no second DB write),
	// Weaviate still receives the target lexical update and succeeds.
	if err := service.Enrich(t.Context(), "org", "receipt"); err != nil {
		t.Fatalf("attempt 2 retry failed: %v", err)
	}
	if baseline.updateCalls != 1 {
		t.Fatalf("retry must not rewrite unchanged Postgres derivation: updateCalls=%d, want 1", baseline.updateCalls)
	}
	if indexer.lexicalCalls != 2 || len(indexer.lastLexical) != 1 {
		t.Fatalf("retry must converge Weaviate lexical update: lexicalCalls=%d updates=%+v", indexer.lexicalCalls, indexer.lastLexical)
	}

	gotLex := indexer.lastSeg.Segments[0].Derivation.LexicalText
	wantLex := "long article\neditorial piece\nin-depth report"
	if gotLex != wantLex {
		t.Fatalf("LexicalText = %q, want %q", gotLex, wantLex)
	}
	if strings.Contains(gotLex, "pastry") {
		t.Fatalf("off-topic synthetic query bypassed cosine-salience gate: %q", gotLex)
	}
	if indexer.lastSeg.Segments[0].Text != "a long article" {
		t.Fatalf("Segment.Text must remain immutable: got %q", indexer.lastSeg.Segments[0].Text)
	}
	if len(indexer.lastData) != 1 || !slices.Equal(indexer.lastData[0].Vector, []float32{1, 0}) {
		t.Fatalf("dense embedding must remain untouched: got %+v", indexer.lastData)
	}
}

// When LexicalEnricher is nil (default off), Enrich uses the baseline
// IndexEmbeddings path with zero extra storage or projection overhead.
func TestEnrichDisabledLexicalEnrichmentHasZeroOverhead(t *testing.T) {
	store := &oneVersion{}
	baseline := &lexicalMemoryBaseline{stored: map[string]content.Segmentation{}}
	artifacts := &memoryArtifacts{byDerivation: map[string]content.Embedding{}, blobs: map[string][]byte{}}
	calls := 0
	p := fillPlugin{segments: []content.SegmentInput{{PartKey: "body", End: 14, LexicalText: "long article"}}, calls: &calls}
	indexer := &recordingLexicalIndexer{}
	service := processing.Service{
		Content:    content.Service{Receipts: store, RecordStore: store, Versions: store, Blobs: store, Baseline: baseline, Embeddings: store},
		Plugin:     &processing.PluginDeriver{Plugin: p, Content: content.Service{Baseline: baseline, Embeddings: artifacts, Blobs: artifacts}},
		Routing:    generationRoute{generation: content.Generation{ID: "generation", SpaceID: "p.large@1"}},
		Enrichment: indexer,
	}
	if err := service.Enrich(t.Context(), "org", "receipt"); err != nil {
		t.Fatalf("Enrich failed: %v", err)
	}
	if baseline.updateCalls != 0 || indexer.lexicalCalls != 0 || indexer.indexCalls != 1 {
		t.Fatalf("disabled lexical enrichment touched lexical path: pgUpdates=%d lexicalCalls=%d indexCalls=%d",
			baseline.updateCalls, indexer.lexicalCalls, indexer.indexCalls)
	}
}

// Stage-1 baseline processing (retrieval_ready) must never invoke
// LexicalEnricher, guaranteeing zero latency added to initial searchability.
func TestBaselineRunNeverInvokesLexicalEnricher(t *testing.T) {
	store := &baselineVersion{}
	contents := content.Service{Materialization: store, Receipts: store, RecordStore: store, Versions: store, Blobs: store, Baseline: store}
	enricher := &staticLexicalEnricher{fn: func(processing.LexicalEnrichmentRequest) map[string]processing.SegmentLexicalExpansion {
		return nil
	}}
	service := processing.Service{
		Content:         contents,
		Routing:         generationRoute{generation: content.Generation{ID: "generation", SpaceID: "example.baseline.text@1"}},
		Retrieval:       baselineIndex{},
		Plugin:          &processing.PluginDeriver{Content: contents, Plugin: baselineOwner{}},
		LexicalEnricher: enricher,
	}
	if err := service.Run(t.Context(), "org", "receipt"); err != nil {
		t.Fatalf("baseline Run failed: %v", err)
	}
	if enricher.calls != 0 {
		t.Fatalf("Stage-1 baseline invoked LexicalEnricher %d times, want 0", enricher.calls)
	}
}

// A transient QueryEncoder failure during cosine-salience gating must return a
// retryable error without persisting partial lexical updates to Postgres.
func TestEnrichTransientQueryEncoderErrorSkipsPostgresWrite(t *testing.T) {
	m := content.Manifest{Kind: "manifest", Parts: []content.Part{{Key: "body", Role: "body", Content: content.Text{Kind: "text", Text: "a long article"}}}}
	store := &oneVersionWithManifest{m: m}
	baseline := &lexicalMemoryBaseline{stored: map[string]content.Segmentation{}}
	artifacts := &memoryArtifacts{byDerivation: map[string]content.Embedding{}, blobs: map[string][]byte{}}
	calls := 0
	p := fillPlugin{segments: []content.SegmentInput{{PartKey: "body", End: 14, LexicalText: "long article"}}, calls: &calls}
	indexer := &recordingLexicalIndexer{}
	doc2query := &staticLexicalEnricher{fn: func(req processing.LexicalEnrichmentRequest) map[string]processing.SegmentLexicalExpansion {
		return map[string]processing.SegmentLexicalExpansion{
			req.Segmentation.Segments[0].ID: {Queries: []processing.ScoredLexicalCandidate{{Text: "synthetic query"}}},
		}
	}}
	service := processing.Service{
		Content:         content.Service{Receipts: store, RecordStore: store, Versions: store, Blobs: store, Baseline: baseline, Embeddings: store},
		Plugin:          &processing.PluginDeriver{Plugin: p, Content: content.Service{Baseline: baseline, Embeddings: artifacts, Blobs: artifacts}},
		Routing:         generationRoute{generation: content.Generation{ID: "generation", SpaceID: "p.large@1"}},
		Enrichment:      indexer,
		LexicalEnricher: doc2query,
		LexicalOptions:  processing.LexicalEnrichmentOptions{QueryEncoder: &staticQueryEncoder{err: errors.New("encoder unavailable")}},
	}
	if err := service.Enrich(t.Context(), "org", "receipt"); err == nil {
		t.Fatal("transient QueryEncoder outage must return a retryable error")
	}
	if baseline.updateCalls != 0 || indexer.lexicalCalls != 0 || indexer.indexCalls != 0 {
		t.Fatalf("failed query encoding wrote storage or projection: pg=%d lexical=%d index=%d",
			baseline.updateCalls, indexer.lexicalCalls, indexer.indexCalls)
	}

	// An ineligible (withdrawn/superseded) Version skips LexicalEnricher and Postgres writes.
	store.ineligible = true
	called := 0
	service.LexicalEnricher = &staticLexicalEnricher{fn: func(processing.LexicalEnrichmentRequest) map[string]processing.SegmentLexicalExpansion {
		called++
		return nil
	}}
	if err := service.Enrich(t.Context(), "org", "receipt"); err != nil {
		t.Fatalf("ineligible Enrich returned error: %v", err)
	}
	if called != 0 || baseline.updateCalls != 0 {
		t.Fatalf("ineligible version invoked lexical enricher: calls=%d pgUpdates=%d", called, baseline.updateCalls)
	}
}
