package weaviate_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	wv "github.com/weaviate/weaviate-go-client/v6"
	"github.com/weaviate/weaviate-go-client/v6/query"
	"github.com/weaviate/weaviate-go-client/v6/query/filter"
	pb "github.com/weaviate/weaviate/grpc/generated/protocol/v1"

	"github.com/The-Vibe-Company/quivr/internal/adapters/weaviate"
	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
)

// attachFixture is one collection with lexical segments projected the way
// promotion projects them, before any embedding is attached.
type attachFixture struct {
	t   *testing.T
	ctx context.Context
	// index is the real index; raw reads what the adapter stored without
	// going through the adapter.
	index    index
	raw      *wv.Client
	store    *weaviate.Store
	gen      content.Generation
	org      string
	corpusID string
}

func newAttachFixture(t *testing.T) *attachFixture {
	t.Helper()
	idx := realIndex(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	f := &attachFixture{t: t, ctx: ctx, index: idx, raw: idx.client(t), store: idx.open(t), org: "adapter-attach", corpusID: "corpus-attach"}
	collection := "QuivrAttach" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if err := f.store.Bootstrap(ctx, collection); err != nil {
		t.Fatal(err)
	}
	f.gen = content.Generation{ID: "generation-attach", Collection: collection, ProfileVersion: retrieval.ProfileVersion, SpaceID: "space-attach"}
	return f
}

func (f *attachFixture) segmentation(id, text string) content.Segmentation {
	return content.Segmentation{ID: "seg-" + id, VersionID: "version-" + id, Segments: []content.Segment{{ID: id, PartKey: "body", Text: text}}}
}

func (f *attachFixture) publish(g content.Generation, seg content.Segmentation) {
	f.t.Helper()
	if err := f.store.Publish(f.ctx, g, f.org, f.corpusID, "example-feed", content.Version{ID: seg.VersionID}, seg); err != nil {
		f.t.Fatal(err)
	}
}

func (f *attachFixture) embedding(g content.Generation, segmentID string, vector []float32) content.EmbeddingData {
	raw, err := content.VectorBytes(vector)
	if err != nil {
		f.t.Fatal(err)
	}
	return content.EmbeddingData{Artifact: content.Embedding{Organization: f.org, SpaceID: g.SpaceID, SegmentID: segmentID, Payload: content.Blob{SHA256: content.Hash(raw)}}, Vector: vector}
}

type storedObject struct {
	ID      string
	Updated string
	Vector  bool
}

// textEquals selects objects whose text properties equal the given
// name/value pairs.
func textEquals(pairs ...string) filter.Expr {
	operands := filter.And{}
	for i := 0; i < len(pairs); i += 2 {
		operands = append(operands, filter.Cond{Operator: filter.Equal, Target: pairs[i], Value: pairs[i+1]})
	}
	return operands
}

// objects lists every physical object projected for a segment in a generation.
func (f *attachFixture) objects(g content.Generation, segmentID string) []storedObject {
	f.t.Helper()
	result, err := f.raw.Collections.Use(g.Collection).Query.OverAll(f.ctx, query.OverAll{
		Filter:           textEquals("organization", f.org, "generationId", g.ID, "segmentId", segmentID),
		Limit:            20,
		ReturnProperties: []string{"segmentId"},
		ReturnVectors:    []string{"semantic_text_v1"},
		ReturnMetadata:   query.ReturnMetadata{LastUpdateAt: true},
	})
	if err != nil {
		f.t.Fatalf("list objects: %v", err)
	}
	list := []storedObject{}
	for _, o := range result.Objects {
		updated := ""
		if o.LastUpdatedAt != nil {
			updated = strconv.FormatInt(o.LastUpdatedAt.UnixMilli(), 10)
		}
		list = append(list, storedObject{ID: o.UUID.String(), Updated: updated, Vector: len(o.Vectors["semantic_text_v1"].Single) > 0})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return list
}

// shape counts a segment's lexical and enriched objects.
func shape(objects []storedObject) (lexical, enriched int) {
	for _, o := range objects {
		if o.Vector {
			enriched++
		} else {
			lexical++
		}
	}
	return lexical, enriched
}

func lexicalObject(objects []storedObject) storedObject {
	for _, o := range objects {
		if !o.Vector {
			return o
		}
	}
	return storedObject{}
}

func (f *attachFixture) search(mode string, vector []float32) map[string]int {
	f.t.Helper()
	q := retrieval.Request{Query: "lanterne", Mode: mode, Profile: "default", Limit: 10, CorpusIDs: []string{f.corpusID}, Vector: vector}
	candidates, err := f.store.Search(f.ctx, []retrieval.Route{{CorpusID: f.corpusID, Generation: f.gen}}, corpus.Scope{Organization: f.org, Corpora: []string{"*"}}, q)
	if err != nil {
		f.t.Fatal(err)
	}
	seen := map[string]int{}
	for _, c := range candidates {
		seen[c.SegmentID]++
	}
	return seen
}

// Attaching a vector must never hide an already searchable segment from
// lexical or hybrid search. Weaviate re-indexes an updated object under a new
// document id, so an in-place update leaves a window where BM25 misses it.
func TestEmbeddingAttachmentKeepsSegmentsLexicallyVisible(t *testing.T) {
	f := newAttachFixture(t)
	segments := []string{"segment-lantern-a", "segment-lantern-b"}
	f.publish(f.gen, f.segmentation(segments[0], "la lanterne rouge du port"))
	f.publish(f.gen, f.segmentation(segments[1], "une lanterne dans la nuit"))
	for i := 0; i < 20; i++ {
		f.publish(f.gen, f.segmentation("segment-filler-"+strconv.Itoa(i), "texte de remplissage numero "+strconv.Itoa(i)))
	}
	anchors := map[string]storedObject{}
	for _, id := range segments {
		anchors[id] = lexicalObject(f.objects(f.gen, id))
	}
	// Re-attachment is bounded by count, not by time, and exercises the same
	// write as the first attachment each time.
	const attachments = 60
	var wg sync.WaitGroup
	done := make(chan struct{})
	errs := make(chan error, len(segments))
	for i, id := range segments {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			for n := 1; n <= attachments; n++ {
				if err := f.store.PublishEmbeddings(f.ctx, f.gen, f.org, []content.EmbeddingData{f.embedding(f.gen, id, unitVector(i*1000+n))}); err != nil {
					errs <- err
					return
				}
			}
		}(i, id)
	}
	go func() { wg.Wait(); close(done) }()
	queries, missing := 0, map[string]int{}
	for running := true; running; {
		select {
		case <-done:
			running = false
		default:
		}
		for _, mode := range []string{"lexical", "hybrid"} {
			seen := f.search(mode, unitVector(3))
			queries++
			for _, id := range segments {
				if seen[id] == 0 {
					missing[mode]++
				}
			}
		}
	}
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	t.Logf("queries=%d missing=%v", queries, missing)
	if len(missing) > 0 {
		t.Fatalf("searchable segments went missing during embedding attachment: %v out of %d queries", missing, queries)
	}
	for i, id := range segments {
		objects := f.objects(f.gen, id)
		if lexical, enriched := shape(objects); lexical != 1 || enriched != 1 {
			t.Fatalf("segment %s projected as %+v, want its lexical object plus one enriched object", id, objects)
		}
		if anchor := lexicalObject(objects); anchor != anchors[id] {
			t.Fatalf("segment %s lexical object changed from %+v to %+v", id, anchors[id], anchor)
		}
		// The surviving object carries the last attached vector.
		if seen := f.search("semantic", unitVector(i*1000+attachments)); seen[id] == 0 {
			t.Fatalf("semantic search with the last vector did not find %s", id)
		}
	}
}

func TestEmbeddingAttachmentConvergesAfterInterruptedSwap(t *testing.T) {
	f := newAttachFixture(t)
	id := "segment-interrupted"
	f.publish(f.gen, f.segmentation(id, "une lanterne interrompue"))
	if err := f.store.PublishEmbeddings(f.ctx, f.gen, f.org, []content.EmbeddingData{f.embedding(f.gen, id, unitVector(41))}); err != nil {
		t.Fatal(err)
	}
	data := []content.EmbeddingData{f.embedding(f.gen, id, unitVector(42))}
	// The cleanup request is dropped, as a crash between inserting the
	// enriched object and removing the stale one would.
	proxy := f.index.proxy(t)
	proxy.Before = func(request wireRequest) error {
		if strings.HasPrefix(request.Name, "DELETE ") {
			return errInjected
		}
		return nil
	}
	crashing := proxy.open(t)
	if err := crashing.PublishEmbeddings(f.ctx, f.gen, f.org, data); err == nil {
		t.Fatal("attachment must report the interrupted cleanup")
	}
	if seen := f.search("lexical", nil); seen[id] == 0 {
		t.Fatalf("interrupted attachment hid the segment: %v", seen)
	}
	// The retry finds the enriched object already written and still cleans up.
	if err := f.store.PublishEmbeddings(f.ctx, f.gen, f.org, data); err != nil {
		t.Fatal(err)
	}
	objects := f.objects(f.gen, id)
	if lexical, enriched := shape(objects); lexical != 1 || enriched != 1 {
		t.Fatalf("retry left %+v, want the lexical object plus one enriched object", objects)
	}
	// Repeating a completed attachment writes nothing.
	if err := f.store.PublishEmbeddings(f.ctx, f.gen, f.org, data); err != nil {
		t.Fatal(err)
	}
	if again := f.objects(f.gen, id); fmt.Sprint(again) != fmt.Sprint(objects) {
		t.Fatalf("idempotent attachment rewrote the object: %+v then %+v", objects, again)
	}
	// Re-publishing the lexical projection after enrichment writes nothing either.
	f.publish(f.gen, f.segmentation(id, "une lanterne interrompue"))
	if again := f.objects(f.gen, id); fmt.Sprint(again) != fmt.Sprint(objects) {
		t.Fatalf("lexical republication touched the enriched object: %+v then %+v", objects, again)
	}
}

func TestEmbeddingAttachmentRequiresProjectedSegment(t *testing.T) {
	f := newAttachFixture(t)
	id := "segment-never-projected"
	err := f.store.PublishEmbeddings(f.ctx, f.gen, f.org, []content.EmbeddingData{f.embedding(f.gen, id, unitVector(7))})
	if !errors.Is(err, retrieval.ErrProjectionMissing) {
		t.Fatalf("attachment to an unprojected segment = %v, want ErrProjectionMissing", err)
	}
	if objects := f.objects(f.gen, id); len(objects) != 0 {
		t.Fatalf("attachment created %+v for an unprojected segment", objects)
	}
}

// Enriched object identities are scoped to their generation and segment, so a
// swap never removes another generation's or another Version's objects.
func TestEmbeddingAttachmentIsScopedToGenerationAndSegment(t *testing.T) {
	f := newAttachFixture(t)
	other := f.gen
	other.ID = "generation-attach-next"
	current, correction := f.segmentation("segment-v1", "une lanterne ancienne"), f.segmentation("segment-v2", "une lanterne corrigee")
	f.publish(f.gen, current)
	f.publish(f.gen, correction)
	f.publish(other, current)
	before := f.objects(f.gen, correction.Segments[0].ID)
	vector := unitVector(9)
	if err := f.store.PublishEmbeddings(f.ctx, other, f.org, []content.EmbeddingData{f.embedding(other, current.Segments[0].ID, vector)}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.PublishEmbeddings(f.ctx, f.gen, f.org, []content.EmbeddingData{f.embedding(f.gen, current.Segments[0].ID, vector)}); err != nil {
		t.Fatal(err)
	}
	a, b := f.objects(f.gen, current.Segments[0].ID), f.objects(other, current.Segments[0].ID)
	la, ea := shape(a)
	lb, eb := shape(b)
	if la != 1 || ea != 1 || lb != 1 || eb != 1 {
		t.Fatalf("each generation must hold the lexical object plus one enriched object: %+v %+v", a, b)
	}
	ids := map[string]bool{}
	for _, o := range append(a, b...) {
		ids[o.ID] = true
	}
	if len(ids) != 4 {
		t.Fatalf("generations share an object identity: %+v %+v", a, b)
	}
	if objects := f.objects(f.gen, correction.Segments[0].ID); fmt.Sprint(objects) != fmt.Sprint(before) {
		t.Fatalf("another Version's lexical object changed: %+v", objects)
	}
}

// Overlapping attachments of the same embedding never delete each other's
// enriched object, and both converge on it. Weaviate indexes an object's id
// after its other properties, so while the first attachment creates the
// enriched object, a filter on the segment matches it but cannot exclude it
// by id yet. The second attachment runs inside that window; a long body keeps
// the window open, and a segment whose properties were indexed in another
// order is retried with the next one.
func TestConcurrentIdenticalAttachmentsConverge(t *testing.T) {
	f := newAttachFixture(t)
	words := make([]string, 20000)
	for i := range words {
		words[i] = "lanterne" + strconv.Itoa(i)
	}
	text := strings.Join(words, " ")
	for n := 0; n < 40; n++ {
		id := "segment-concurrent-" + strconv.Itoa(n)
		f.publish(f.gen, f.segmentation(id, text))
		data := []content.EmbeddingData{f.embedding(f.gen, id, unitVector(11+n))}
		first := make(chan error, 1)
		go func() { first <- f.store.PublishEmbeddings(f.ctx, f.gen, f.org, data) }()
		overlapped, err := f.awaitUnindexedIdentity(id, first)
		if overlapped {
			second := f.store.PublishEmbeddings(f.ctx, f.gen, f.org, data)
			if err = <-first; err == nil {
				err = second
			}
		}
		if err != nil {
			t.Fatalf("attachment %s: %v", id, err)
		}
		if objects := f.objects(f.gen, id); fmt.Sprint(shape(objects)) != "1 1" {
			t.Fatalf("attachment %s left %+v, want the lexical object plus one enriched object", id, objects)
		}
		if overlapped {
			return
		}
	}
	t.Fatal("no attachment overlapped the indexing of its enriched object")
}

// awaitUnindexedIdentity polls until the segment's enriched object matches a
// filter that excludes its own id, or the attachment creating it returns.
func (f *attachFixture) awaitUnindexedIdentity(segmentID string, done chan error) (bool, error) {
	f.t.Helper()
	for {
		select {
		case err := <-done:
			return false, err
		default:
		}
		for _, o := range f.objects(f.gen, segmentID) {
			if o.Vector && f.matchesExcluding(segmentID, o.ID) {
				return true, nil
			}
		}
	}
}

// matchesExcluding reports whether a filter on the segment that excludes id
// still returns id.
func (f *attachFixture) matchesExcluding(segmentID, id string) bool {
	f.t.Helper()
	excluded, err := uuid.Parse(id)
	if err != nil {
		f.t.Fatal(err)
	}
	result, err := f.raw.Collections.Use(f.gen.Collection).Query.OverAll(f.ctx, query.OverAll{
		Filter: filter.And{
			filter.Cond{Operator: filter.Equal, Target: "segmentId", Value: segmentID},
			filter.Cond{Operator: filter.Operator(pb.Filters_OPERATOR_NOT_EQUAL), Target: filter.UUID, Value: excluded},
		},
		Limit:            20,
		ReturnProperties: []string{},
	})
	if err != nil {
		f.t.Fatalf("filter objects: %v", err)
	}
	for _, o := range result.Objects {
		if o.UUID == excluded {
			return true
		}
	}
	return false
}
