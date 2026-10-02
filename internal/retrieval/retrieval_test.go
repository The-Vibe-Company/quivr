package retrieval_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
)

const segmentText = "une lanterne"

// fakeRouting routes every Corpus to one generation; unprojected models a
// generation built before Source Namespaces were projected.
type fakeRouting struct{ unprojected bool }

func (fakeRouting) Authorize(context.Context, corpus.Scope, []string) error { return nil }
func (f fakeRouting) Generation(context.Context, string, string) (content.Generation, error) {
	return content.Generation{ID: "gen", Collection: "Shared", ProfileVersion: retrieval.ProfileVersion, SpaceID: "space", SourceNamespaceProjected: !f.unprojected}, nil
}

type fakeEmbedder struct{}

func (fakeEmbedder) Embed(context.Context, string) ([]float32, error) { return []float32{1}, nil }
func (fakeEmbedder) Space() content.VectorSpace                       { return content.VectorSpace{ID: "space"} }

type fakeProjection struct {
	candidates []content.Candidate
	fetch      int
	attachErr  error
	attached   int
	searched   []retrieval.Request
}

func (f *fakeProjection) Publish(context.Context, content.Generation, string, string, string, content.Version, content.Segmentation) error {
	return nil
}
func (f *fakeProjection) PublishEmbeddings(context.Context, content.Generation, string, []content.EmbeddingData) error {
	f.attached++
	return f.attachErr
}

// Search returns at most CandidateLimit candidates, as the adapter does.
func (f *fakeProjection) Search(_ context.Context, _ []retrieval.Route, _ corpus.Scope, q retrieval.Request) ([]content.Candidate, error) {
	f.searched = append(f.searched, q)
	f.fetch = retrieval.CandidateLimit
	if len(f.candidates) > retrieval.CandidateLimit {
		return f.candidates[:retrieval.CandidateLimit], nil
	}
	return f.candidates, nil
}

type fakeRecords struct{ content.RecordReader }

func (fakeRecords) Record(_ context.Context, _ string, id string) (content.Record, error) {
	return content.Record{ID: id, Source: content.Source{CorpusID: "corpus"}}, nil
}

// fakeBaseline finds every candidate but stale- ones, each in its own blob,
// and counts the batch lookups it answers.
type fakeBaseline struct {
	content.BaselineRepository
	lookups *atomic.Int64
}

func (f fakeBaseline) Hydrate(_ context.Context, _ corpus.Scope, cs []content.Candidate) (map[int]content.Located, error) {
	if f.lookups != nil {
		f.lookups.Add(1)
	}
	out := map[int]content.Located{}
	for i, c := range cs {
		if strings.HasPrefix(c.SegmentID, "stale-") {
			continue
		}
		h := content.Hydrated{GenerationID: c.GenerationID, RecordID: "record-" + c.SegmentID, VersionID: "version-" + c.SegmentID, TextSHA256: content.Hash([]byte(segmentText)), Segment: content.Segment{ID: c.SegmentID, PartKey: "body", End: len([]rune(segmentText))}}
		// A vec- segment holds a vector in the served space.
		if strings.HasPrefix(c.SegmentID, "vec-") {
			h.EmbeddingID, h.SpaceID = "embedding-"+c.SegmentID, "space"
		}
		out[i] = content.Located{Hydrated: h, Blob: content.Blob{Key: "blob-" + c.SegmentID}}
	}
	return out, nil
}

type fakeBlobs struct{ content.Blobs }

func (fakeBlobs) Read(context.Context, content.Blob) ([]byte, error) { return []byte(segmentText), nil }

// countingBlobs counts the segment texts read: one per hydrated candidate.
type countingBlobs struct {
	content.Blobs
	reads *atomic.Int64
}

func (b countingBlobs) Read(context.Context, content.Blob) ([]byte, error) {
	b.reads.Add(1)
	return []byte(segmentText), nil
}

type fakeEmbeddings struct {
	content.EmbeddingRepository
	eligible []bool
	checks   int
	commits  int
}

func (f *fakeEmbeddings) EnrichmentEligible(context.Context, string, string) (bool, error) {
	v := f.eligible[0]
	if len(f.eligible) > 1 {
		f.eligible = f.eligible[1:]
	}
	f.checks++
	return v, nil
}
func (f *fakeEmbeddings) CommitEnrichment(context.Context, string, content.Segmentation, content.Generation, []content.Embedding) error {
	f.commits++
	return nil
}

// service answers searches through a retrieval plugin that asks, like
// core.retrieve, for the candidates of the search's mode and ranks them as
// served.
func service(p *fakeProjection, e *fakeEmbeddings) retrieval.Service {
	return retrieval.Service{Embedder: fakeEmbedder{}, Routing: fakeRouting{}, Projection: p, Ranker: &scriptedRanker{answer: passthrough}, Registry: spaceList{},
		Content: content.Service{RecordStore: fakeRecords{}, Baseline: fakeBaseline{}, Blobs: fakeBlobs{}, Embeddings: e}}
}

var searchScope = corpus.Scope{Organization: "org", Actions: []string{"content:read", "search:query"}, Corpora: []string{"*"}}

// An enriched segment has a lexical and an enriched object, and a third while
// its embedding is replaced. Every matching segment in that state at once is the
// worst case: a full page of candidates must still be served.
func TestSearchFillsPageWhileSegmentsHaveDuplicateObjects(t *testing.T) {
	p := &fakeProjection{}
	for i := 0; i < retrieval.MaxLimit+10; i++ {
		id := "segment-" + strconv.Itoa(i)
		for n := 0; n < 3; n++ {
			p.candidates = append(p.candidates, content.Candidate{SegmentID: id, GenerationID: "gen"})
		}
	}
	out, err := service(p, &fakeEmbeddings{}).Search(context.Background(), searchScope, retrieval.Request{Query: "lanterne", Mode: "lexical", Limit: retrieval.MaxLimit, CorpusIDs: []string{"corpus"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Hits) != retrieval.MaxLimit {
		t.Fatalf("page of %d hits from %d fetched candidates, want %d", len(out.Hits), p.fetch, retrieval.MaxLimit)
	}
	seen := map[string]bool{}
	for _, h := range out.Hits {
		if seen[h.Segment.ID] {
			t.Fatalf("segment %s returned twice", h.Segment.ID)
		}
		seen[h.Segment.ID] = true
	}
}

// Objects of superseded or withdrawn Versions are not filtered by the projection
// query; hydration drops them. They consume the fetched objects, so a page is
// full only while live segments fit in them (docs/quivr-v2-remaining-limits.md).
// However many it drops, a page costs a few batch lookups, not one per candidate.
func TestSearchStaleCandidatesConsumeTheCandidateBudget(t *testing.T) {
	const stale = retrieval.CandidateLimit - retrieval.MaxLimit
	build := func(stale int) *fakeProjection {
		p := &fakeProjection{}
		for i := 0; i < stale; i++ {
			p.candidates = append(p.candidates, content.Candidate{SegmentID: "stale-" + strconv.Itoa(i), GenerationID: "gen"})
		}
		for i := 0; i < retrieval.MaxLimit+10; i++ {
			p.candidates = append(p.candidates, content.Candidate{SegmentID: "segment-" + strconv.Itoa(i), GenerationID: "gen"})
		}
		return p
	}
	// A lookup and a recheck per batch: the k best, then, with all of them
	// dropped, every candidate left.
	const maxLookups = 4
	search := func(p *fakeProjection) int {
		t.Helper()
		lookups := &atomic.Int64{}
		s := service(p, &fakeEmbeddings{})
		s.Content.Baseline = fakeBaseline{lookups: lookups}
		out, err := s.Search(context.Background(), searchScope, retrieval.Request{Query: "lanterne", Mode: "lexical", Limit: retrieval.MaxLimit, CorpusIDs: []string{"corpus"}})
		if err != nil {
			t.Fatal(err)
		}
		if lookups.Load() > maxLookups {
			t.Fatalf("a page of %d hits took %d storage lookups, want at most %d", len(out.Hits), lookups.Load(), maxLookups)
		}
		return len(out.Hits)
	}
	if got := search(build(stale)); got != retrieval.MaxLimit {
		t.Fatalf("%d stale candidates within the budget left a page of %d, want %d", stale, got, retrieval.MaxLimit)
	}
	if got := search(build(stale + 10)); got != retrieval.MaxLimit-10 {
		t.Fatalf("stale candidates beyond the budget left a page of %d, want the documented shortfall %d", got, retrieval.MaxLimit-10)
	}
}

// The index query of a candidate request does not depend on its k, so the
// served order is the one the engine's own search had for any limit; and
// serving reads from storage only the candidates it keeps.
func TestServingAsksTheSameIndexQueryAndReadsOnlyWhatItKeeps(t *testing.T) {
	p := &fakeProjection{}
	for i := 0; i < retrieval.CandidateLimit; i++ {
		p.candidates = append(p.candidates, content.Candidate{SegmentID: "segment-" + strconv.Itoa(i), GenerationID: "gen"})
	}
	reads := &atomic.Int64{}
	s := service(p, &fakeEmbeddings{})
	s.Content.Blobs = countingBlobs{reads: reads}
	out, err := s.Search(context.Background(), searchScope, retrieval.Request{Query: "lanterne", Mode: "hybrid", Limit: 10, CorpusIDs: []string{"corpus"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.searched) != 1 || p.searched[0].K != retrieval.CandidateLimit || p.searched[0].Hybrid == nil || p.searched[0].Hybrid.Alpha != 0.5 || p.searched[0].Hybrid.Fusion != retrieval.FusionRelativeScore {
		t.Fatalf("projection searched %+v; want one hybrid query of %d objects, alpha 0.5, relative score", p.searched, retrieval.CandidateLimit)
	}
	if len(out.Hits) != 10 || out.Hits[0].Segment.ID != "segment-0" || out.Hits[9].Segment.ID != "segment-9" || reads.Load() != 10 {
		t.Fatalf("%d hits after %d storage reads; want the first 10 in index order, reading 10", len(out.Hits), reads.Load())
	}
}

// A source filter reaches the projection query, and is refused rather than
// silently narrowed when the routed generation's objects lack Source Namespaces.
func TestSearchSourceFilterNeedsProjectedGeneration(t *testing.T) {
	q := retrieval.Request{Query: "lanterne", Mode: "lexical", CorpusIDs: []string{"corpus"}, SourceNamespaces: []string{"feed-a", "feed-b"}}
	p := &fakeProjection{candidates: []content.Candidate{{SegmentID: "segment", GenerationID: "gen"}}}
	if _, err := service(p, &fakeEmbeddings{}).Search(context.Background(), searchScope, q); err != nil {
		t.Fatal(err)
	}
	if len(p.searched) != 1 || strings.Join(p.searched[0].SourceNamespaces, ",") != "feed-a,feed-b" {
		t.Fatalf("projection queried with %+v, want the source filter feed-a,feed-b", p.searched)
	}
	legacy := service(p, &fakeEmbeddings{})
	legacy.Routing = fakeRouting{unprojected: true}
	if _, err := legacy.Search(context.Background(), searchScope, q); !errors.Is(err, retrieval.ErrSourceFilterUnavailable) {
		t.Fatalf("filtered search on an unprojected generation = %v, want ErrSourceFilterUnavailable", err)
	}
	if len(p.searched) != 1 {
		t.Fatal("a refused filtered search must not query the projection")
	}
	q.SourceNamespaces = nil
	if _, err := legacy.Search(context.Background(), searchScope, q); err != nil {
		t.Fatalf("unfiltered search on an unprojected generation = %v, want it served", err)
	}
	for _, namespaces := range [][]string{{"feed-a", "feed-a"}, {""}} {
		q.SourceNamespaces = namespaces
		if _, err := service(p, &fakeEmbeddings{}).Search(context.Background(), searchScope, q); !errors.Is(err, retrieval.ErrUnsupported) {
			t.Errorf("source filter %q = %v, want ErrUnsupported", namespaces, err)
		}
	}
}

func unit() []float32 {
	v := make([]float32, 384)
	v[0] = 1
	return v
}

func attachment(t *testing.T) (content.Version, content.Segmentation, []content.EmbeddingData) {
	t.Helper()
	v := content.Version{ID: "version", RecordID: "record"}
	seg := content.Segmentation{ID: "seg", VersionID: v.ID, Segments: []content.Segment{{ID: "segment"}}}
	raw, err := content.VectorBytes(unit())
	if err != nil {
		t.Fatal(err)
	}
	e := content.Embedding{Organization: "org", VersionID: v.ID, SegmentationID: seg.ID, SegmentID: "segment", SpaceID: "space", Payload: content.Blob{SHA256: content.Hash(raw)}}
	return v, seg, []content.EmbeddingData{{Artifact: e, Vector: unit()}}
}

func TestEnrichmentOfIneligibleVersionEndsWithoutTouchingProjection(t *testing.T) {
	v, seg, data := attachment(t)
	p, e := &fakeProjection{}, &fakeEmbeddings{eligible: []bool{false}}
	if err := service(p, e).IndexEmbeddings(context.Background(), "org", v, seg, data); err != nil {
		t.Fatalf("ineligible enrichment = %v, want it to end", err)
	}
	if p.attached != 0 || e.commits != 0 {
		t.Fatalf("ineligible enrichment attached %d and committed %d", p.attached, e.commits)
	}
}

func TestEnrichmentEndsWhenVersionLeavesEligibilityDuringAttachment(t *testing.T) {
	v, seg, data := attachment(t)
	p, e := &fakeProjection{attachErr: retrieval.ErrProjectionMissing}, &fakeEmbeddings{eligible: []bool{true, false}}
	if err := service(p, e).IndexEmbeddings(context.Background(), "org", v, seg, data); err != nil {
		t.Fatalf("enrichment of a Version withdrawn mid-attachment = %v, want it to end", err)
	}
	if e.commits != 0 {
		t.Fatal("a withdrawn Version must not commit enrichment")
	}
}

func TestEnrichmentOfEligibleVersionWithMissingProjectionRetries(t *testing.T) {
	v, seg, data := attachment(t)
	p, e := &fakeProjection{attachErr: retrieval.ErrProjectionMissing}, &fakeEmbeddings{eligible: []bool{true}}
	if err := service(p, e).IndexEmbeddings(context.Background(), "org", v, seg, data); !errors.Is(err, retrieval.ErrProjectionMissing) {
		t.Fatalf("eligible Version without projection = %v, want a retryable error", err)
	}
}

func TestEnrichmentOfEligibleVersionCommits(t *testing.T) {
	v, seg, data := attachment(t)
	p, e := &fakeProjection{}, &fakeEmbeddings{eligible: []bool{true}}
	if err := service(p, e).IndexEmbeddings(context.Background(), "org", v, seg, data); err != nil {
		t.Fatal(err)
	}
	if p.attached != 1 || e.commits != 1 {
		t.Fatalf("attached %d, committed %d; want 1 and 1", p.attached, e.commits)
	}
}
